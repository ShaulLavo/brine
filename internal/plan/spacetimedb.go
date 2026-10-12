package plan

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

const ManagedEngineUnsupported ConflictCode = "managed_engine_not_admitted"

type ManagedBlueprint struct {
	EngineHostPort  target.Port             `json:"engine_host_port"`
	AppHostPort     target.Port             `json:"app_host_port,omitempty"`
	ClientPrefix    string                  `json:"client_prefix"`
	ClientDatabase  string                  `json:"client_database"`
	EngineImage     spec.ImageReference     `json:"engine_image"`
	EngineVersion   string                  `json:"engine_version"`
	EngineResources spec.SpacetimeResources `json:"engine_resources"`
	AppResources    *policy.Resources       `json:"app_resources,omitempty"`
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
	if in.Managed != nil {
		managed, err := in.Managed.CanonicalBytes()
		if err != nil {
			return Plan{}, err
		}
		snapshot, err = json.Marshal(struct {
			Snapshot json.RawMessage `json:"snapshot"`
			Managed  json.RawMessage `json:"managed"`
		}{snapshot, managed})
		if err != nil {
			return Plan{}, err
		}
	}
	d := in.Desired
	image := in.Image
	if d.App().HasAppContainer() && (!image.validManifest() || !validHash(image.Digest) || !strings.HasSuffix(string(d.Image), "@"+image.Digest)) {
		return Plan{}, errors.New("managed preview: frontend image metadata must match its pinned digest")
	}
	if !d.App().HasAppContainer() {
		image = Image{Digest: strings.SplitN(string(d.SpacetimeDB.Image), "@", 2)[1], Platform: target.Platform{OS: "linux", Arch: in.Snapshot.Arch}, ManifestDigest: target.Observation[string]{Status: target.Unknown}}
	}
	p := Plan{SchemaVersion: SchemaVersion, Kind: Conflict, App: string(d.Name), Image: image, Target: in.Snapshot.Identity, ObservedGeneration: in.Snapshot.Generation, PolicyVersion: d.PolicyVersion, PolicyHash: d.PolicyHash, DesiredHash: hash(desired), DatabaseEnvironment: d.DatabaseEnvironmentValues(), Secrets: []SecretBinding{}, Changes: []Change{}, Conflicts: []Diagnostic{{Code: ManagedEngineUnsupported, Field: "databases.spacetimedb"}}}
	if d.App().HasAppContainer() && (image.Platform.OS != "linux" || image.Platform.Arch != in.Snapshot.Arch) {
		p.Conflicts = append(p.Conflicts, Diagnostic{Code: ImagePlatform, Field: "image.platform"})
	}
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
	blueprint := &ManagedBlueprint{ClientPrefix: d.SpacetimeDB.ClientPrefix(), ClientDatabase: d.SpacetimeDB.Database, EngineImage: d.SpacetimeDB.Image, EngineVersion: d.SpacetimeDB.Version, EngineResources: d.SpacetimeDB.Resources}
	if d.App().HasAppContainer() {
		resources := d.Resources
		blueprint.AppResources = &resources
	}
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
			if !committedPortReserved(in, app) {
				return nil, false
			}
			busy[app.CommittedPortReservation.Port] = true
		}
	}
	for _, release := range in.State.Releases {
		busy[release.HostPort] = true
	}
	for _, reservation := range in.State.ManagedReservations {
		busy[reservation.EnginePort] = true
		busy[reservation.FrontendPort] = true
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

func committedPortReserved(in Input, app target.App) bool {
	r := app.CommittedPortReservation
	if r == nil || !app.UnitsAbsent() || in.State.Target != in.Snapshot.Identity || in.Snapshot.Generation.Value == nil || in.Snapshot.Generation.Status != target.KnownStatus || in.State.Generation != *in.Snapshot.Generation.Value {
		return false
	}
	for _, reservation := range in.State.ManagedReservations {
		if reservation.App == app.Name {
			return false
		}
	}
	for _, release := range in.State.Releases {
		if release.App == app.Name && release.ID == r.ReleaseID && release.HostPort == r.Port && release.Managed == nil {
			return true
		}
	}
	return false
}
