package plan

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

const PrepareManagedEngine ChangeKind = "prepare_managed_engine"

// BuildManagedPreparation freezes an unpublished allocation. It has no engine,
// frontend, upload, publication, routing, or serving-release changes.
func BuildManagedPreparation(in Input, proposed ManagedServicePlan, root data.RootEvidence) (Plan, error) {
	if policy.ValidateManagedDesired(in.Desired) != nil || in.Managed == nil || in.Managed.Runtime != nil || proposed.State != data.EngineUnpublished || !proposed.Valid() || proposed.Database.Name != in.Desired.SpacetimeDB.Name || proposed.Database.Root != in.Desired.SpacetimeDB.PersistentRoot || proposed.Archive.Destination != in.Desired.BackupDestinations[0] || !slices.Contains(in.Desired.PersistentRoots, proposed.Database.Root) {
		return Plan{}, errors.New("managed unpublished preparation required")
	}
	if in.Desired.App().HasAppContainer() != (proposed.Frontend != nil) || proposed.EngineImage.Platform.Arch != in.Snapshot.Arch || proposed.Frontend != nil && (proposed.Frontend.Image.Platform.Arch != in.Snapshot.Arch || proposed.Frontend.Image.Digest != in.Image.Digest) {
		return Plan{}, errors.New("managed preparation components differ")
	}
	if in.Managed.Database != nil && (*in.Managed.Database != proposed.Database || in.Managed.Archive == nil || *in.Managed.Archive != proposed.Archive) {
		return Plan{}, errors.New("managed preparation reservation changed")
	}
	desired, err := in.Desired.CanonicalBytes()
	if err != nil {
		return Plan{}, err
	}
	snapshot, err := canonicalDecisionFacts(in)
	if err != nil {
		return Plan{}, err
	}
	state, err := canonicalState(in.State)
	if err != nil {
		return Plan{}, err
	}
	managedFacts, err := in.Managed.CanonicalBytes()
	if err != nil {
		return Plan{}, err
	}
	snapshot, err = json.Marshal(struct {
		Snapshot json.RawMessage `json:"snapshot"`
		Managed  json.RawMessage `json:"managed"`
	}{snapshot, managedFacts})
	if err != nil {
		return Plan{}, err
	}
	frozen := proposed.Clone()
	primary := frozen.EngineImage
	port := frozen.EnginePort
	if frozen.Frontend != nil {
		primary = frozen.Frontend.Image
		port = frozen.Frontend.Port
	}
	p := Plan{SchemaVersion: SchemaVersion, Kind: Create, App: string(in.Desired.Name), Target: in.Snapshot.Identity, ObservedGeneration: in.Snapshot.Generation, PolicyVersion: in.Desired.PolicyVersion, PolicyHash: in.Desired.PolicyHash, DesiredHash: hash(desired), Image: primary, HostPort: port, Managed: &frozen, MappingImage: data.MappingProbeImage, Secrets: []SecretBinding{}, Changes: []Change{{Kind: PrepareManagedEngine}}, Conflicts: []Diagnostic{}}
	add := func(code ConflictCode, field string) {
		p.Conflicts = append(p.Conflicts, Diagnostic{Code: code, Field: field})
	}
	deploymentCapabilities(in.Snapshot, add)
	if in.Snapshot.Generation.Status != target.KnownStatus || in.Snapshot.Generation.Value == nil {
		add(UnknownFacts, "generation")
	}
	if in.State.Target != in.Snapshot.Identity || in.Snapshot.Generation.Value != nil && in.State.Generation != *in.Snapshot.Generation.Value {
		add(StaleState, "brine_state")
	}
	if proposed.EnginePort < in.Desired.AppPorts.Min || proposed.EnginePort > in.Desired.AppPorts.Max || proposed.Frontend != nil && (proposed.Frontend.Port < in.Desired.AppPorts.Min || proposed.Frontend.Port > in.Desired.AppPorts.Max) {
		add(PortOwned, "managed.ports")
	}
	ports, known := previewPorts(in)
	if !known {
		add(UnknownFacts, "ports")
	} else if !slices.Contains(ports, proposed.EnginePort) || proposed.Frontend != nil && !slices.Contains(ports, proposed.Frontend.Port) {
		add(PortOwned, "managed.ports")
	}
	for _, release := range in.State.Releases {
		if release.App == p.App {
			add(ArtifactDrift, "managed.preparation.release")
		}
	}
	for _, incarnation := range in.State.DataIncarnations {
		if incarnation.App == p.App && incarnation.ID != string(proposed.Database.IncarnationID) {
			add(ArtifactDrift, "managed.preparation.incarnation")
		}
	}
	if root.Root != proposed.Database.Root || root.Device == 0 || root.Inode == 0 || root.ObservedAt.IsZero() {
		add(UnknownFacts, "managed.preparation.root")
	}
	if root.FreeBytes < in.Desired.MinimumFreeDiskBytes || root.Filesystem != "btrfs" && root.FreeInodes == 0 {
		add(InsufficientDisk, "managed.preparation.root")
	}
	root.ObservedAt = time.Time{}
	root.FreeBytes = in.Desired.MinimumFreeDiskBytes
	root.FreeInodes = 0
	root.OperatorQuota = nil
	p.PreparationRoots = []data.RootEvidence{root}
	return finish(p, desired, snapshot, state)
}
