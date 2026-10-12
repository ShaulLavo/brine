package plan

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

const ManagedPublicationRequired ConflictCode = "managed_publication_required"
const ManagedServiceChangeRequired ConflictCode = "managed_service_change_required"
const ManagedBackupRevisionRequired ConflictCode = "managed_backup_revision_required"
const InstallManagedEngine ChangeKind = "install_managed_engine"
const StartManagedEngine ChangeKind = "start_managed_engine"
const DeployManagedFrontend ChangeKind = "deploy_managed_frontend"
const PublishManagedIngress ChangeKind = "publish_managed_ingress"

func buildManagedDeployment(in Input, desired []byte) (Plan, error) {
	if policy.ValidateManagedDesired(in.Desired) != nil || in.Managed == nil || in.Managed.Database == nil || in.Managed.Archive == nil || in.Managed.Runtime == nil {
		return Plan{}, errors.New("initialized managed facts required")
	}
	facts := in.Managed.Clone()
	runtime := facts.Runtime
	if runtime.Validate() != nil || runtime.State != data.EngineInitialized || facts.Archive.Validate(*facts.Database) != nil || runtime.BindingID != facts.Database.BindingID || runtime.EpochID != facts.Archive.EpochID || !managedEngineImageVerified(facts.EngineImage) {
		return Plan{}, errors.New("managed runtime ownership differs")
	}
	raw, err := json.Marshal(runtime.Clone())
	if err != nil {
		return Plan{}, err
	}
	managed := ManagedServicePlan{Database: *facts.Database, Archive: *facts.Archive, State: data.EngineInitialized, EngineImage: facts.EngineImage, EnginePort: target.Port(runtime.HostPort), RuntimeSHA256: strings.TrimPrefix(hash(raw), "sha256:"), EngineUnitHash: runtime.UnitSHA256}
	p := Plan{SchemaVersion: SchemaVersion, Kind: Create, App: string(in.Desired.Name), Target: in.Snapshot.Identity, ObservedGeneration: in.Snapshot.Generation, PolicyVersion: in.Desired.PolicyVersion, PolicyHash: in.Desired.PolicyHash, DesiredHash: hash(desired), Image: facts.EngineImage, HostPort: managed.EnginePort, Secrets: []SecretBinding{}, Changes: []Change{}, Conflicts: []Diagnostic{}}
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
	for _, fence := range facts.Fences {
		if fence.State == data.FenceHeld {
			add(ArtifactDrift, "managed.fence")
		}
	}
	if facts.NativeState.Status != target.KnownStatus || facts.NativeState.Value == nil || *facts.NativeState.Value != "running" && *facts.NativeState.Value != "stopped" {
		add(UnknownFacts, "managed.native_state")
	}
	if facts.LoggingPrerequisite.Status != target.KnownStatus || facts.LoggingPrerequisite.Value == nil || !*facts.LoggingPrerequisite.Value {
		add(UnknownFacts, "managed.ingress_logging")
	}
	engine := in.Desired.SpacetimeDB
	if managed.Database.Name != engine.Name || managed.Database.Root != engine.PersistentRoot || managed.Archive.Destination != in.Desired.BackupDestinations[0] {
		return Plan{}, errors.New("managed deployment allocation differs")
	}
	metadata := engine.Module.Metadata
	if metadata == nil || string(metadata.RuntimeModuleHash) != string(runtime.Module.Program) || string(metadata.WasmSHA256) != string(runtime.Module.Wasm) || metadata.Canonicalizer != runtime.Module.Schema.Canonicalizer || metadata.SchemaSHA256 != runtime.Module.Schema.SHA256 || engine.Module.Marker != runtime.Module.Schema.Marker || !slices.Contains(engine.Module.Accepts, runtime.Module.Schema.Marker) || !slices.Contains(runtime.Names, engine.Database) {
		add(ManagedPublicationRequired, "databases.module")
	}
	if string(engine.Image) != runtime.Image || engine.Version != runtime.Version || engine.Runtime != runtime.Runtime || engine.Resources.MemoryMB != runtime.MemoryMB || engine.Resources.PIDsLimit != runtime.PIDsLimit || engine.Resources.PagePoolMB != runtime.PagePoolMB || engine.Resources.StopTimeoutSeconds != runtime.StopSeconds || string(engine.Publisher) != runtime.Secrets.Publisher.Reference || string(engine.SigningPrivate) != runtime.Secrets.SigningPrivate.Reference || string(engine.SigningPublic) != runtime.Secrets.SigningPublic.Reference || string(engine.Auth) != runtime.Secrets.AuthPolicy.Reference {
		add(ManagedServiceChangeRequired, "databases.runtime")
	}
	var previous *CurrentRelease
	for i := range in.State.Releases {
		if in.State.Releases[i].App == p.App {
			previous = &in.State.Releases[i]
		}
	}
	if previous != nil {
		p.Kind = Update
		if previous.Managed == nil || previous.Managed.Database != managed.Database || previous.Managed.Archive != managed.Archive {
			add(ArtifactDrift, "managed.release")
		}
	}
	if in.Desired.App().HasAppContainer() {
		frontend, conflicts := managedFrontend(in, previous, managed.EnginePort)
		p.Conflicts = append(p.Conflicts, conflicts...)
		managed.Frontend = &frontend
		p.Image = frontend.Image
		p.HostPort = frontend.Port
		p.Secrets = slices.Clone(frontend.Secrets)
	}
	p.Managed = &managed
	p.ConfigHash = configHash(in.Desired, p.Image, p.HostPort, p.Secrets)
	if previous != nil {
		p.Diff = configurationDiff(in.Desired, p.Image, p.HostPort, p.Secrets, previous)
	}
	if previous != nil && reflect.DeepEqual(previous.Desired.App(), in.Desired.App()) && (!reflect.DeepEqual(previous.Desired.SpacetimePolicy, in.Desired.SpacetimePolicy) || !reflect.DeepEqual(previous.Desired.BackupRetention, in.Desired.BackupRetention)) {
		add(ManagedBackupRevisionRequired, "managed.backup_policy")
	}
	if previous != nil && previous.Managed != nil && previous.Managed.RuntimeSHA256 == managed.RuntimeSHA256 && reflect.DeepEqual(previous.Managed.Frontend, managed.Frontend) && configHash(previous.Desired, previous.Image, previous.HostPort, previous.Secrets) == p.ConfigHash && facts.NativeState.Value != nil && *facts.NativeState.Value == "running" {
		p.Kind = NoOp
	}
	if p.Kind != NoOp {
		if facts.NativeState.Value != nil && *facts.NativeState.Value == "stopped" {
			p.Changes = append(p.Changes, Change{Kind: InstallManagedEngine}, Change{Kind: StartManagedEngine})
		}
		if managed.Frontend != nil {
			p.Changes = append(p.Changes, Change{Kind: DeployManagedFrontend})
		} else if previous != nil && previous.Managed != nil && previous.Managed.Frontend != nil {
			p.Changes = append(p.Changes, Change{Kind: StopManagedFrontend}, Change{Kind: RemoveManagedFrontend})
		}
		p.Changes = append(p.Changes, Change{Kind: PublishManagedIngress})
	}
	if !managed.Valid() {
		// A conflict has no executable owned component record. Partial desired
		// frontend observations cannot masquerade as a valid service plan.
		p.Managed = nil
	}
	snapshot, err := canonicalDecisionFacts(in)
	if err != nil {
		return Plan{}, err
	}
	encoded, err := facts.CanonicalBytes()
	if err != nil {
		return Plan{}, err
	}
	snapshot, err = json.Marshal(struct {
		Snapshot json.RawMessage `json:"snapshot"`
		Managed  json.RawMessage `json:"managed"`
	}{snapshot, encoded})
	if err != nil {
		return Plan{}, err
	}
	state, err := canonicalState(in.State)
	if err != nil {
		return Plan{}, err
	}
	return finish(p, desired, snapshot, state)
}
