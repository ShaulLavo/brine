package plan

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
)

var ErrPersistentData = result.New(result.PersistentArchiveRequired, nil)

type ReleaseIdentity struct {
	App string `json:"app"`
	ID  string `json:"id"`
}

// Removal binds the destructive operation to immutable committed ownership.
// History and secret versions remain retained under D5, not deleted with heads.
type Removal struct {
	ReleaseID string                `json:"release_id"`
	Units     []target.Unit         `json:"units"`
	Route     target.CaddyFile      `json:"route"`
	Routing   target.CaddyConfigSet `json:"routing"`
	Releases  []ReleaseIdentity     `json:"releases"`
}

func BuildRemove(in Input) (Plan, error) {
	if !in.Desired.Stateless() {
		return Plan{}, ErrPersistentData
	}
	for _, r := range in.State.Releases {
		if r.App == string(in.Desired.Name) && !r.Desired.Stateless() {
			return Plan{}, ErrPersistentData
		}
	}
	desired, err := in.Desired.CanonicalBytes()
	if err != nil {
		return Plan{}, err
	}
	raw, err := target.Encode(in.Snapshot)
	if err != nil {
		return Plan{}, err
	}
	if err = json.Unmarshal(raw, &in.Snapshot); err != nil {
		return Plan{}, err
	}
	state, err := canonicalState(in.State)
	if err != nil {
		return Plan{}, err
	}
	if err = json.Unmarshal(state, &in.State); err != nil {
		return Plan{}, err
	}
	facts, err := canonicalDecisionFacts(in.Snapshot, in.Desired.MinimumFreeDiskBytes)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{SchemaVersion: SchemaVersion, Kind: NoOp, Lifecycle: RemoveApp, App: string(in.Desired.Name), Target: in.Snapshot.Identity, ObservedGeneration: in.Snapshot.Generation, PolicyVersion: in.Desired.PolicyVersion, PolicyHash: in.Desired.PolicyHash, DesiredHash: hash(desired), Image: in.Image, Secrets: []SecretBinding{}, Changes: []Change{}, Conflicts: []Diagnostic{}}
	conflict := func(field string) {
		p.Kind = Conflict
		p.Conflicts = append(p.Conflicts, Diagnostic{Code: ArtifactDrift, Field: field})
	}
	if !reflect.DeepEqual(in.State.Target, in.Snapshot.Identity) || in.Snapshot.Generation.Value == nil || *in.Snapshot.Generation.Value != in.State.Generation {
		conflict("brine_state")
	}
	var current *CurrentRelease
	identities := []ReleaseIdentity{}
	for i := range in.State.Releases {
		r := &in.State.Releases[i]
		identities = append(identities, ReleaseIdentity{App: r.App, ID: r.ID})
		if r.App == p.App {
			current = r
		}
	}
	if in.Snapshot.Apps.Status != target.KnownStatus || in.Snapshot.Apps.Value == nil {
		conflict("apps")
	}
	var observed *target.App
	if in.Snapshot.Apps.Value != nil {
		for i := range *in.Snapshot.Apps.Value {
			if (*in.Snapshot.Apps.Value)[i].Name == p.App {
				observed = &(*in.Snapshot.Apps.Value)[i]
			}
		}
	}
	cfg := target.CaddyConfigSet{Files: []target.CaddyFile{}}
	if in.Snapshot.CaddyConfig.Status == target.KnownStatus && in.Snapshot.CaddyConfig.Value != nil {
		cfg = *in.Snapshot.CaddyConfig.Value
	} else if in.Snapshot.CaddyConfig.Status != target.Absent {
		conflict("caddy_config")
	}
	if current == nil {
		if observed != nil && (observed.Image.Status != target.Absent || observed.AllocatedHostPort.Status != target.Absent || observed.QuadletUnits.Status != target.KnownStatus || len(*observed.QuadletUnits.Value) != 0 || observed.Secrets.Status != target.KnownStatus) {
			conflict("unowned_app")
		}
		for _, f := range cfg.Files {
			if f.Name == p.App+".caddy" {
				conflict("unowned_route")
			}
		}
	} else {
		for _, unit := range current.Units {
			if strings.HasSuffix(unit.Name, ".volume") {
				return Plan{}, ErrPersistentData
			}
		}
		if len(current.Units) != 1 || current.Units[0].Name != p.App+".container" {
			conflict("units")
		}
		if observed == nil || observed.QuadletUnits.Status != target.KnownStatus || observed.QuadletUnits.Value == nil || !reflect.DeepEqual(*observed.QuadletUnits.Value, current.Units) {
			conflict("units")
		}
		if current.CaddyFile.Name != p.App+".caddy" || !slices.Contains(cfg.Files, current.CaddyFile) {
			conflict("route")
		}
		p.HostPort = current.HostPort
		p.Image = current.Image
		p.Secrets = slices.Clone(current.Secrets)
		p.Removal = &Removal{ReleaseID: current.ID, Units: slices.Clone(current.Units), Route: current.CaddyFile, Routing: cfg, Releases: identities}
		if p.Kind != Conflict {
			p.Kind = Update
			for _, kind := range []ChangeKind{WithdrawRoute, StopApp, RemoveUnit, RetireApp} {
				p.Changes = append(p.Changes, Change{Kind: kind, Removal: &Restart{App: p.App}})
			}
		}
	}
	if p.Kind == Conflict {
		p.Changes = []Change{}
	}
	return finish(p, desired, facts, state)
}
