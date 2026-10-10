package plan

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/ShaulLavo/brine/internal/target"
)

// BuildLifecycle keeps the committed bindings, including old secret versions.
// Creating an unbound secret must not make restart implicitly rotate it.
func BuildLifecycle(in Input, action ChangeKind) (Plan, error) {
	if action == ReviseReplica {
		p, err := Build(in)
		if err != nil {
			return Plan{}, err
		}
		if p.Lifecycle != ReviseReplica {
			return Plan{}, fmt.Errorf("replica revision requires a cadence-only change")
		}
		return p, nil
	}
	if action == RemoveApp {
		return BuildRemove(in)
	}
	if action != RestartApp && action != StopApp && action != StartApp {
		return Plan{}, fmt.Errorf("unsupported lifecycle action")
	}
	var current *CurrentRelease
	for i := range in.State.Releases {
		if in.State.Releases[i].App == string(in.Desired.Name) {
			current = &in.State.Releases[i]
			break
		}
	}
	if current == nil {
		return Plan{}, fmt.Errorf("lifecycle requires a committed release")
	}
	if configurationDiff(in.Desired, in.Image, current.HostPort, current.Secrets, current) != nil || !reflect.DeepEqual(in.Desired.Secrets, current.Desired.Secrets) {
		return Plan{}, fmt.Errorf("lifecycle cannot change configuration")
	}
	original := in.Snapshot
	// Preserve all decision facts for the fingerprint. The normal planner first
	// checks the committed binding rather than selecting a newly staged version.
	raw, err := target.Encode(in.Snapshot)
	if err != nil {
		return Plan{}, err
	}
	in.Snapshot, err = target.Decode(raw)
	if err != nil {
		return Plan{}, err
	}
	if in.Snapshot.Apps.Value != nil {
		for i := range *in.Snapshot.Apps.Value {
			a := &(*in.Snapshot.Apps.Value)[i]
			if a.Name != current.App || a.Secrets.Status != target.KnownStatus || a.Secrets.Value == nil {
				continue
			}
			secrets := []target.Secret{}
			for _, binding := range current.Secrets {
				for _, live := range *a.Secrets.Value {
					if live.Name == binding.VersionName && live.ID == binding.ID {
						secrets = append(secrets, live)
					}
				}
			}
			a.Secrets = target.Known(secrets)
		}
	}
	p, err := Build(in)
	if err != nil {
		return Plan{}, err
	}
	// A stopped unit cannot own a live listener. Keep ordinary deployment
	// planning independent of volatile health, but refuse contradictory lifecycle
	// evidence even if a caller claims the socket belongs to this app.
	if original.Apps.Value != nil && original.PortOwners.Value != nil {
		for _, app := range *original.Apps.Value {
			if app.Name != current.App || app.UnitActive == nil || app.UnitActive.Status != target.KnownStatus || *app.UnitActive.Value {
				continue
			}
			for _, owner := range *original.PortOwners.Value {
				if owner.Port == current.HostPort && owner.App == current.App {
					p.Conflicts = append(p.Conflicts, Diagnostic{Code: PortOwned, Field: "host_port"})
					p.Kind = Conflict
				}
			}
		}
	}
	p.Hash = ""
	p.Lifecycle = action
	p.Diff = nil
	p.Changes = []Change{}
	if p.Kind != Conflict {
		p.Kind = Update
		p.Secrets = slices.Clone(current.Secrets)
		p.ConfigHash = configHash(in.Desired, in.Image, current.HostPort, p.Secrets)
		change := Change{Kind: action}
		switch action {
		case RestartApp:
			change.Restart = &Restart{App: p.App}
		case StopApp:
			change.Stop = &Restart{App: p.App}
		case StartApp:
			change.Start = &Restart{App: p.App}
		}
		p.Changes = []Change{change}
	}
	desired, err := in.Desired.CanonicalBytes()
	if err != nil {
		return Plan{}, err
	}
	facts, err := canonicalDecisionFacts(original, in.Desired.MinimumFreeDiskBytes)
	if err != nil {
		return Plan{}, err
	}
	state, err := canonicalState(in.State)
	if err != nil {
		return Plan{}, err
	}
	return finish(p, desired, facts, state)
}
