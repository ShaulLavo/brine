package apply

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

type revisionPreparationFake struct {
	preparationFake
	resumable         bool
	operation         string
	prepares, resumes int
}

func (f *revisionPreparationFake) PreparePersistent(ctx context.Context, operation string, p plan.Plan, d policy.Desired) error {
	f.operation = operation
	f.prepares++
	err := f.preparationFake.PreparePersistent(ctx, operation, p, d)
	if err == nil {
		f.prepared = true
	}
	return err
}
func (f *revisionPreparationFake) InspectReplicaRevision(context.Context, string, plan.Plan, policy.Desired) (bool, error) {
	return f.resumable, nil
}

func cadenceExecutionRig(t *testing.T) (*rig, *revisionPreparationFake) { return cadenceRig(t, false) }

func cadenceRig(t *testing.T, keepEffects bool) (*rig, *revisionPreparationFake) {
	t.Helper()
	r := newRig(t, true)
	persistent := persistentRecoveryRig(t)
	desired := r.oldDesired
	desired.Runtime = persistent.desired.Runtime
	desired.Backup = persistent.desired.Backup
	desired.Databases = persistent.desired.Databases
	desired.SchemaCompatibility = persistent.desired.SchemaCompatibility
	mount := persistent.plan.DataMounts[0]
	desired.PersistentRoots = []data.PersistentRoot{mount.Database.Root}
	destination := data.Destination{Reference: "primary", Endpoint: "https://storage.example", Region: "region-1", Bucket: "backups", BasePrefix: "brine", CredentialRef: "primary"}
	desired.BackupDestinations = []data.Destination{destination}
	now := time.Now().UTC()
	retention := data.RetentionEvidence{Destination: destination.Reference, Endpoint: destination.Endpoint, Bucket: destination.Bucket, BasePrefix: destination.BasePrefix, VerificationID: strings.Repeat("4", 32), VerifiedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), FreshnessSeconds: 3600, NoObjectExpiration: true}
	desired.BackupRetention = []data.RetentionEvidence{retention}
	root := data.RootEvidence{Root: mount.Database.Root, Device: 1, Inode: 2, Filesystem: "ext4", POSIXLocks: true, DurableRename: true, FreeBytes: desired.MinimumFreeDiskBytes * 2, FreeInodes: 10, ObservedAt: now}
	mapping := data.MappingEvidence{Runtime: *desired.Runtime, RunnerUID: 1000, RunnerGID: 1000, Root: root.Root, Device: root.Device, Image: string(desired.Image), KeepID: true, PrivateModes: true, HostReadWrite: true, ContainerReadWrite: true, ObservedAt: now}
	persistentFacts := target.Known([]target.PersistentDatabase{{Database: mount.Database, Root: root, Mapping: target.Known(mapping), Retention: target.Known(retention), Credentials: target.Known(data.CredentialEvidence{BindingID: mount.BindingID, EpochID: data.ReplicaEpochID(strings.Repeat("5", 32)), Destination: "primary", Reference: "primary", Version: 1, PolicyHash: desired.PolicyHash, ReceivedAt: now.Add(-time.Minute)}), Usage: target.Known(data.StorageUsage{}), Definitions: []data.SchemaDefinition{{Database: "main", Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}}, Schema: data.SchemaObservation{DatabaseID: mount.Database.DatabaseID, State: data.AllocatedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: now}}})
	r.facts.Input.Desired = desired
	r.facts.Input.Snapshot.PersistentData = &persistentFacts
	r.facts.Input.State.Releases[0].Desired = desired
	r.facts.Input.State.Releases[0].CaddyGeneration = r.release.CaddyGeneration
	old, err := plan.Build(r.facts.Input)
	if err != nil || old.Kind != plan.NoOp {
		t.Fatal("old persistent app not verified", old.Conflicts, err)
	}
	unit, err := quadlet.Render(desired, old, *old.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	units := []target.Unit{{Name: unit.Name(), Hash: unit.Hash()}}
	r.facts.Input.State.Releases[0].Units = units
	(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits = target.Known(units)
	r.oldPlan, err = plan.Build(r.facts.Input)
	if err != nil {
		t.Fatal(err)
	}
	r.oldDesired = desired
	r.release.Units = units
	r.release.PlanID = r.oldPlan.Hash
	r.desired = desired
	r.desired.Databases = slices.Clone(desired.Databases)
	r.desired.Databases[0].SyncInterval = 2 * time.Minute
	r.facts.Input.Desired = r.desired
	r.plan, err = plan.Build(r.facts.Input)
	if err != nil || r.plan.Lifecycle != plan.ReviseReplica {
		t.Fatal("cadence plan not replica-only", r.plan.Conflicts, err)
	}
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	f := &revisionPreparationFake{preparationFake: preparationFake{r: r}, resumable: true}
	r.executor.PersistentData = f
	// A cadence lifecycle has no need for any application effects, writer intent,
	// health probe, routes or unit installation adapters.
	if !keepEffects {
		r.executor.Podman = nil
		r.executor.Systemd = nil
		r.executor.Units = nil
		r.executor.Routes = nil
		r.executor.Health = nil
		r.executor.WriterStarts = nil
	}
	return r, f
}

func TestCadenceExecutionCommitsOnlyReleaseMetadata(t *testing.T) {
	r, f := cadenceExecutionRig(t)
	before := r.release
	if err := r.executor.Run(context.Background(), "operation-1", r.plan, r.desired); err != nil {
		t.Fatal(err)
	}
	if r.state != Succeeded || f.calls != 1 || !reflect.DeepEqual(r.effects, []string{"commit"}) {
		t.Fatal("cadence touched app effects", r.state, r.effects)
	}
	after := r.release
	after.ID = before.ID
	after.PlanID = before.PlanID
	if !reflect.DeepEqual(after, before) {
		t.Fatal("metadata commit changed app artifacts")
	}
	if r.release.PlanID != r.plan.Hash || r.release.ID != "operation-1" {
		t.Fatal("desired cadence was not committed")
	}
}

func TestCadenceRecoveryResumesOwnCursorWithoutAppEffects(t *testing.T) {
	ctx := context.Background()
	r, f := cadenceExecutionRig(t)
	f.err = replicaUnknown()
	if err := r.executor.Run(ctx, "operation-1", r.plan, r.desired); err == nil || r.state != RecoveryRequired {
		t.Fatal("unknown replica cursor settled", err)
	}
	source := Operation{ID: "operation-1", Kind: ops.Deploy, App: r.plan.App, PlanID: r.plan.Hash, State: RecoveryRequired}
	successor := Operation{ID: "resolution-1", Kind: ops.Resolve, App: r.plan.App, PlanID: r.plan.Hash, RecoveryOf: source.ID, State: Queued}
	r.operationKind = ops.Resolve
	f.err = nil
	f.resumable = false
	recovery, err := r.executor.InspectResolution(ctx, successor, source, r.plan, r.desired, r.events)
	if err != nil || recovery.Action != RequireRecovery || f.calls != 1 {
		t.Fatal("unsettled replica attempt resumed", err)
	}
	f.resumable = true
	recovery, err = r.executor.InspectResolution(ctx, successor, source, r.plan, r.desired, r.events)
	if err != nil || recovery.Action != ResumeForward || f.calls != 1 {
		t.Fatal("settled replica cursor not resumed read-only", recovery.Action, err)
	}
	r.state = Queued
	if err = r.executor.Recover(ctx, recovery); err != nil {
		t.Fatal(err)
	}
	if r.state != Succeeded || f.operation != source.ID || f.calls != 2 || !reflect.DeepEqual(r.effects, []string{"commit"}) {
		t.Fatal("resolution lost source cursor or touched app", r.state, f.operation, r.effects)
	}
}

func replicaUnknown() error { return &Error{Step: "prepare_data", Code: "interrupted"} }

func TestCadenceMetadataCommitUnknownIsInspected(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_commit", true: "after_commit"}[committed], func(t *testing.T) {
			r, f := cadenceExecutionRig(t)
			r.unknownStep = "commit"
			r.commitThenError = committed
			err := r.executor.Run(context.Background(), "operation-1", r.plan, r.desired)
			if committed {
				if err != nil || r.state != Succeeded {
					t.Fatal("committed metadata outcome not inspected", err)
				}
			} else if err == nil || r.state != RecoveryRequired {
				t.Fatal("uncommitted metadata claimed success", err)
			}
			if f.calls != 1 || !reflect.DeepEqual(r.effects, []string{"commit"}) {
				t.Fatal("unknown metadata changed app effects")
			}
		})
	}
}

func TestCadenceResolutionInspectsCommittedMetadataWithoutRepeatingReplica(t *testing.T) {
	ctx := context.Background()
	r, f := cadenceExecutionRig(t)
	r.failOutcome = "commit"
	if err := r.executor.Run(ctx, "operation-1", r.plan, r.desired); err == nil || r.state != RecoveryRequired || r.release.ID != "operation-1" {
		t.Fatal("lost metadata outcome not retained", err)
	}
	source := Operation{ID: "operation-1", Kind: ops.Deploy, App: r.plan.App, PlanID: r.plan.Hash, State: RecoveryRequired}
	successor := Operation{ID: "resolution-1", Kind: ops.Resolve, App: r.plan.App, PlanID: r.plan.Hash, RecoveryOf: source.ID, State: Queued}
	r.operationKind = ops.Resolve
	r.failOutcome = ""
	recovery, err := r.executor.InspectResolution(ctx, successor, source, r.plan, r.desired, r.events)
	if err != nil || recovery.Action != FinishSucceeded {
		t.Fatal("committed metadata not independently inspected", recovery.Action, err)
	}
	r.state = Queued
	if err = r.executor.Recover(ctx, recovery); err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 || !reflect.DeepEqual(r.effects, []string{"commit"}) {
		t.Fatal("settlement repeated replica or metadata effects")
	}
}

func (f *revisionPreparationFake) ResumeReplicaRevision(ctx context.Context, operation string, p plan.Plan, d policy.Desired) error {
	f.resumes++
	f.operation = operation
	if err := f.preparationFake.PreparePersistent(ctx, operation, p, d); err != nil {
		return err
	}
	f.prepared = true
	return nil
}

func TestCadenceAfterAnotherAppDeploymentPreservesHistoricalGeneration(t *testing.T) {
	for _, lostOutcome := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "recover"}[lostOutcome], func(t *testing.T) {
			r, _ := cadenceExecutionRig(t)
			before := r.release
			other := r.facts.Input.State.Releases[0]
			other.App, other.ID, other.Desired.Name = "other", "other-release", "other"
			other.Desired.Domains = []spec.Domain{"other.example.net"}
			other.Desired.Secrets = []policy.Secret{}
			other.Secrets = []plan.SecretBinding{}
			other.Units = []target.Unit{{Name: "other.container", Hash: "sha256:" + strings.Repeat("e", 64)}}
			other.CaddyFile = target.CaddyFile{Name: "other.caddy", Hash: "sha256:" + strings.Repeat("e", 64)}
			other.CaddyGeneration = before.CaddyGeneration + 1
			r.facts.Input.State.Releases = append(r.facts.Input.State.Releases, other)
			r.facts.Input.Snapshot.CaddyConfig.Value.Generation = other.CaddyGeneration
			r.facts.Input.Snapshot.CaddyConfig.Value.Files = append(r.facts.Input.Snapshot.CaddyConfig.Value.Files, other.CaddyFile)
			*r.facts.Input.Snapshot.LiveCaddyFiles.Value = append(*r.facts.Input.Snapshot.LiveCaddyFiles.Value, target.LiveCaddyFile{Name: other.CaddyFile.Name, App: "other", Domains: target.Known([]string{"other.example.net"})})
			r.facts.Routing.Generation = other.CaddyGeneration
			r.facts.Routing.Files[other.CaddyFile.Name] = other.CaddyFile.Hash
			var err error
			r.plan, err = plan.Build(r.facts.Input)
			if err != nil || r.plan.Lifecycle != plan.ReviseReplica {
				t.Fatal("two-app cadence plan", err, r.plan.Conflicts)
			}
			if lostOutcome {
				r.failOutcome = "commit"
			}
			err = r.executor.Run(context.Background(), "operation-1", r.plan, r.desired)
			if !lostOutcome && err != nil {
				t.Fatal(err)
			}
			if lostOutcome {
				if err == nil || r.state != RecoveryRequired {
					t.Fatal("lost outcome not retained", err)
				}
				r.failOutcome = ""
				source := Operation{ID: "operation-1", Kind: ops.Deploy, App: r.plan.App, PlanID: r.plan.Hash, State: RecoveryRequired}
				successor := Operation{ID: "resolution-1", Kind: ops.Resolve, App: r.plan.App, PlanID: r.plan.Hash, RecoveryOf: source.ID, State: Queued}
				r.operationKind = ops.Resolve
				recovery, err := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, r.events)
				if err != nil || recovery.Action != FinishSucceeded {
					t.Fatal("historical generation blocked recovery", recovery.Action, err)
				}
				r.state = Queued
				if err = r.executor.Recover(context.Background(), recovery); err != nil {
					t.Fatal(err)
				}
			}
			if r.release.CaddyGeneration != before.CaddyGeneration || !reflect.DeepEqual(r.release.Units, before.Units) || r.facts.Routing.Generation != other.CaddyGeneration || r.facts.Routing.Files[other.CaddyFile.Name] != other.CaddyFile.Hash {
				t.Fatal("cadence rewrote routing history or other app")
			}
		})
	}
}

func TestMixedCadenceRecoveryResumesOnlyReplicaCursor(t *testing.T) {
	for _, boundary := range []string{"service_replacement", "binding_commit", "daemon_reload"} {
		t.Run(boundary, func(t *testing.T) {
			r, f := cadenceRig(t, true)
			r.desired.Environment = []policy.Environment{{Name: "APP_ENV", Value: "updated"}}
			r.facts.Input.Desired = r.desired
			var err error
			r.plan, err = plan.Build(r.facts.Input)
			if err != nil || r.plan.Lifecycle != "" {
				t.Fatal("not a mixed deploy", err)
			}
			r.executor.WriterStarts = &writerStartsFake{}
			f.err = replicaUnknown()
			if err = r.executor.Run(context.Background(), "operation-1", r.plan, r.desired); err == nil || r.state != RecoveryRequired {
				t.Fatal("replica interruption lost", err)
			}
			source := Operation{ID: "operation-1", Kind: ops.Deploy, App: r.plan.App, PlanID: r.plan.Hash, State: RecoveryRequired}
			successor := Operation{ID: "resolution-1", Kind: ops.Resolve, App: r.plan.App, PlanID: r.plan.Hash, RecoveryOf: source.ID, State: Queued}
			r.operationKind = ops.Resolve
			f.resumable = false
			recovery, err := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, r.events)
			if err != nil || recovery.Action != RequireRecovery {
				t.Fatal("unsettled cursor resumed", recovery.Action, err)
			}
			f.resumable, f.err = true, nil
			recovery, err = r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, r.events)
			if err != nil || recovery.Action != ResumeForward {
				t.Fatal("settled cursor not resumed", recovery.Action, err)
			}
			r.state = Queued
			if err = r.executor.Recover(context.Background(), recovery); err != nil {
				t.Fatal(err)
			}
			if f.prepares != 1 || f.resumes != 1 || f.operation != source.ID || r.state != Succeeded {
				t.Fatal("unrelated preparation replayed or source cursor lost", f.prepares, f.resumes, f.operation, r.state)
			}
		})
	}
}
