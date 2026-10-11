package plan

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

const ManagedEngineUnsupported ConflictCode = "managed_engine_not_admitted"

type ManagedBlueprint struct {
	EngineHostPort target.Port         `json:"engine_host_port"`
	AppHostPort    target.Port         `json:"app_host_port,omitempty"`
	ClientPrefix   string              `json:"client_prefix"`
	ClientDatabase string              `json:"client_database"`
	EngineImage    spec.ImageReference `json:"engine_image"`
}

// ManagedRenderPreview is renderer input only. No apply engine consumes it.
type ManagedRenderPreview struct {
	desired    policy.Desired
	enginePort spec.Port
	appPort    spec.Port
}

func NewManagedRenderPreview(d policy.Desired, p policy.Policy, enginePort, appPort spec.Port) (ManagedRenderPreview, error) {
	if policy.ValidateManagedDesired(d) != nil {
		return ManagedRenderPreview{}, errors.New("managed preview: invalid desired input")
	}
	normalized, err := policy.Normalize(d.App(), p)
	if err != nil {
		return ManagedRenderPreview{}, err
	}
	wanted, err := d.CanonicalBytes()
	if err != nil {
		return ManagedRenderPreview{}, err
	}
	actual, err := normalized.CanonicalBytes()
	if err != nil || !bytes.Equal(wanted, actual) {
		return ManagedRenderPreview{}, errors.New("managed preview: normalized input differs")
	}
	if enginePort < d.AppPorts.Min || enginePort > d.AppPorts.Max {
		return ManagedRenderPreview{}, errors.New("managed preview: engine port is not allowed")
	}
	if d.App().HasAppContainer() {
		if appPort < d.AppPorts.Min || appPort > d.AppPorts.Max || appPort == enginePort {
			return ManagedRenderPreview{}, errors.New("managed preview: app port is not allowed")
		}
	} else if appPort != 0 {
		return ManagedRenderPreview{}, errors.New("managed preview: engine-only input cannot allocate an app port")
	}
	return ManagedRenderPreview{desired: normalized, enginePort: enginePort, appPort: appPort}, nil
}

func (p ManagedRenderPreview) Values() (policy.Desired, spec.Port, spec.Port, error) {
	if policy.ValidateManagedDesired(p.desired) != nil || p.enginePort == 0 {
		return policy.Desired{}, 0, 0, errors.New("managed preview required")
	}
	raw, err := p.desired.CanonicalBytes()
	if err != nil {
		return policy.Desired{}, 0, 0, err
	}
	var detached policy.Desired
	if json.Unmarshal(raw, &detached) != nil {
		return policy.Desired{}, 0, 0, errors.New("managed preview invalid")
	}
	return detached, p.enginePort, p.appPort, nil
}

func buildManagedPreview(in Input, desired []byte) (Plan, error) {
	if policy.ValidateManagedDesired(in.Desired) != nil {
		return Plan{}, errors.New("managed preview: invalid desired input")
	}
	snapshot, err := target.Encode(in.Snapshot)
	if err != nil {
		return Plan{}, err
	}
	state, err := canonicalState(in.State)
	if err != nil {
		return Plan{}, err
	}
	d := in.Desired
	p := Plan{SchemaVersion: SchemaVersion, Kind: Conflict, App: string(d.Name), Target: in.Snapshot.Identity, ObservedGeneration: in.Snapshot.Generation, PolicyVersion: d.PolicyVersion, PolicyHash: d.PolicyHash, DesiredHash: hash(desired), DatabaseEnvironment: d.DatabaseEnvironmentValues(), Secrets: []SecretBinding{}, Changes: []Change{}, Conflicts: []Diagnostic{{Code: ManagedEngineUnsupported, Field: "databases.spacetimedb"}}}
	ports, known := previewPorts(in)
	needed := 1
	if d.App().HasAppContainer() {
		needed++
	}
	if !known {
		p.Conflicts = append(p.Conflicts, Diagnostic{Code: UnknownFacts, Field: "ports"})
	}
	if known && len(ports) < needed {
		p.Conflicts = append(p.Conflicts, Diagnostic{Code: PortsExhausted, Field: "ports"})
	}
	blueprint := &ManagedBlueprint{ClientPrefix: d.SpacetimeDB.ClientPrefix(), ClientDatabase: d.SpacetimeDB.Database, EngineImage: d.SpacetimeDB.Image}
	if known && len(ports) >= needed {
		blueprint.EngineHostPort = ports[0]
		if needed == 2 {
			blueprint.AppHostPort, p.HostPort = ports[1], ports[1]
		}
	}
	p.ManagedPreview = blueprint
	return finish(p, desired, snapshot, state)
}

func previewPorts(in Input) ([]target.Port, bool) {
	s := in.Snapshot
	if s.UsedPorts.Status != target.KnownStatus || s.UsedPorts.Value == nil || s.PortOwners.Status != target.KnownStatus || s.PortOwners.Value == nil || s.Apps.Status != target.KnownStatus || s.Apps.Value == nil {
		return nil, false
	}
	busy := map[target.Port]bool{80: true, 443: true, 2019: true}
	for _, port := range *s.UsedPorts.Value {
		busy[port] = true
	}
	for _, owner := range *s.PortOwners.Value {
		busy[owner.Port] = true
	}
	for _, app := range *s.Apps.Value {
		if app.AllocatedHostPort.Status == target.KnownStatus && app.AllocatedHostPort.Value != nil {
			busy[*app.AllocatedHostPort.Value] = true
		}
		if app.AllocatedHostPort.Status == target.Unknown {
			return nil, false
		}
	}
	for _, release := range in.State.Releases {
		busy[release.HostPort] = true
	}
	available := []target.Port{}
	for port := uint32(in.Desired.AppPorts.Min); port <= uint32(in.Desired.AppPorts.Max); port++ {
		if !busy[target.Port(port)] {
			available = append(available, target.Port(port))
		}
		if len(available) == 2 {
			break
		}
	}
	return available, true
}
