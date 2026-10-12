//go:build linux

package host

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

func incarnationInitializationRig(t *testing.T) (*deployRig, datainit.Service, datainit.Request) {
	t.Helper()
	r, _, _, _ := connectedPreparationRig(t)
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), "Registry.Example.com:5000", "ghcr.io"))
	app, err := spec.Parse([]byte(r.spec))
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), `persistent_roots = ["/srv/brine/data"]`, fmt.Sprintf("persistent_roots = [%q]", app.Databases[0].PersistentRoot)))
	raw = append(raw, []byte(fmt.Sprintf(`
[data_initialization]
max_backup_age_seconds=300
max_restore_test_age_seconds=300
recovery_window_seconds=3600
[[backup_destinations]]
reference="primary"
endpoint="https://storage.example"
region="region-1"
bucket="backups"
base_prefix="brine"
credential_ref="primary"
[[backup_retention]]
destination="primary"
endpoint="https://storage.example"
bucket="backups"
base_prefix="brine"
verification_id="11111111111111111111111111111111"
verified_at=%q
freshness_seconds=3600
no_object_expiration=true
`, time.Now().UTC().Format(time.RFC3339Nano)))...)
	r.policy.p, err = policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	r.service.Requester = initializationRequester(t).String()
	initializer := data.SchemaInitializer{Definition: data.SQLiteSchemaDefinition{Database: "main", Marker: "v1", CatalogSHA256: "688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"}, Statements: []string{"CREATE TABLE t(x TEXT)"}}
	artifact, bytes, err := initializer.Artifact()
	if err != nil {
		t.Fatal(err)
	}
	app.SchemaDefinitions = []data.SQLiteSchemaDefinition{initializer.Definition}
	app.SchemaArtifacts = []data.SchemaArtifact{artifact}
	app.Initializers = []data.SchemaInitializer{initializer}
	app.SchemaCompatibility[0].Accepts = []string{initializer.Definition.Marker}
	preparation, err := r.service.PlanDataPreparation(t.Context(), app)
	if err != nil {
		t.Fatal(err)
	}
	accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: preparation.PlanID, IdempotencyKey: "init-incarnation"}).Data.(jobs.Accepted)
	if err = r.runner.Run(t.Context(), accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	engine := dataInitializationService(r.service, t.TempDir())
	engine.PrepareRestorePoint = func(_ context.Context, p datainit.Plan, op datainit.Operation) (datainit.VerifiedRestorePoint, error) {
		return initializationRecoveryPoint(p, op), nil
	}
	return r, engine, datainit.Request{App: "hello", FirstReleasePlan: preparation.PlanID, Artifact: artifact.Digest, ArtifactBytes: bytes}
}

func initializedApp(t *testing.T, r *deployRig, request datainit.Request) spec.App {
	t.Helper()
	app, err := spec.Parse([]byte(r.spec))
	if err != nil {
		t.Fatal(err)
	}
	initializer, err := datainit.DecodeArtifact(request.ArtifactBytes, request.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	app.SchemaDefinitions = []data.SQLiteSchemaDefinition{initializer.Definition}
	app.SchemaArtifacts = []data.SchemaArtifact{{Definition: initializer.Definition, Digest: request.Artifact}}
	app.Initializers = []data.SchemaInitializer{initializer}
	app.SchemaCompatibility[0].Accepts = []string{initializer.Definition.Marker}
	return app
}

func TestInitializationSameNamedRuntime(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*target.App)
		allowed bool
	}{
		{name: "retired", allowed: true},
		{name: "live-image", mutate: func(a *target.App) { a.Image = target.Known(target.Image{}) }},
		{name: "retained-port", mutate: func(a *target.App) { a.AllocatedHostPort = target.Known(target.Port(20000)) }},
		{name: "unknown-image", mutate: func(a *target.App) { a.Image = target.Observation[target.Image]{Status: target.Unknown} }},
		{name: "unknown-port", mutate: func(a *target.App) { a.AllocatedHostPort = target.Observation[target.Port]{Status: target.Unknown} }},
		{name: "unknown-units", mutate: func(a *target.App) { a.QuadletUnits = target.Observation[[]target.Unit]{Status: target.Unknown} }},
		{name: "missing-unit-evidence", mutate: func(a *target.App) { a.QuadletUnits = target.Observation[[]target.Unit]{Status: target.KnownStatus} }},
		{name: "retained-secret", allowed: true, mutate: func(a *target.App) {
			a.Secrets = target.Known([]target.Secret{{Name: "brine.hello.token.v1", ID: "retained"}})
		}},
		{name: "unknown-active", mutate: func(a *target.App) {
			active := target.Observation[bool]{Status: target.Unknown}
			a.UnitActive = &active
		}},
		{name: "active", mutate: func(a *target.App) { active := target.Known(true); a.UnitActive = &active }},
		{name: "owned-unit", mutate: func(a *target.App) { a.QuadletUnits = target.Known([]target.Unit{{Name: "hello.container"}}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, engine, request := incarnationInitializationRig(t)
			app := target.App{Name: "hello", Image: target.Observation[target.Image]{Status: target.Absent}, AllocatedHostPort: target.Observation[target.Port]{Status: target.Absent}, QuadletUnits: target.Known([]target.Unit{}), Secrets: target.Known([]target.Secret{})}
			if test.mutate != nil {
				test.mutate(&app)
			}
			r.inventory.snapshot.Apps = target.Known([]target.App{app})
			planned, err := engine.Plan(t.Context(), request)
			if (err == nil) != test.allowed {
				t.Fatalf("initialization allowed=%t: %v", test.allowed, err)
			}
			if test.allowed {
				if _, err = engine.Apply(t.Context(), request.App, planned.ID); err != nil {
					t.Fatal("retired app refused during fresh quiescence", err)
				}
			}
		})
	}
}

func retireInitializedIncarnation(t *testing.T, r *deployRig, request datainit.Request) (data.AppIncarnationID, store.Release) {
	t.Helper()
	ctx := t.Context()
	app := initializedApp(t, r, request)
	planned, err := r.service.Plan(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := r.store.LoadPlan(ctx, planned.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	release := store.Release{ID: "old-release", PlanID: planned.PlanID, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: []target.Unit{{Name: "hello.container", Hash: "sha256:" + strings.Repeat("b", 64)}}, CaddyFile: target.CaddyFile{Name: "hello.caddy", Hash: "sha256:" + strings.Repeat("c", 64)}}
	if err = r.store.CommitRelease(ctx, request.App, release); err != nil {
		t.Fatal(err)
	}
	candidate, err := r.store.ArchiveCandidate(ctx, request.App)
	if err != nil {
		t.Fatal(err)
	}
	retention, ok := r.policy.p.BackupRetention("primary")
	if !ok {
		t.Fatal("missing retention")
	}
	candidate.Databases[0] = candidate.Databases[0].WithRetention([]data.RetentionEvidence{retention})
	id, err := data.NewID()
	if err != nil {
		t.Fatal(err)
	}
	db := sqliteArchiveDatabase(t, candidate.Databases[0])
	allocation := db.Allocation
	generation, err := r.store.Generation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	archivePlan := archive.Plan{ArchiveID: data.ArchiveID(id), Requester: r.service.Requester, PolicyHash: r.policy.p.Hash(), TargetHash: "sha256:" + strings.Repeat("d", 64), Generation: generation, CreatedAt: time.Now().UTC(), Candidate: candidate, Trees: []data.ArchiveTree{{Root: db.Binding.Root, RootDevice: allocation.RootDevice, RootInode: allocation.RootInode, SourceDevice: allocation.DirectoryDevice, SourceInode: allocation.DirectoryInode, SourceRelative: "apps/" + string(candidate.Incarnation), ArchiveRelative: "archives/" + id}}}
	archivePlan.ID, err = archivePlan.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err = r.store.SaveArchivePlan(ctx, archivePlan); err != nil {
		t.Fatal(err)
	}
	operation, err := r.store.ClaimArchive(ctx, archivePlan)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []archive.Stage{archive.WritersStopIntent, archive.WritersStopped, archive.CoverageIntent} {
		operation, err = r.store.AdvanceArchive(ctx, operation, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	observation := data.ObserveSQLiteSchemaWithAllocation(ctx, db.Binding, db.Definitions, db.Allocation)
	epoch := db.Epochs[0]
	pointID, err := data.NewID()
	if err != nil {
		t.Fatal(err)
	}
	point := data.SQLiteRestorePoint{ID: pointID, BindingID: epoch.BindingID, EpochID: epoch.EpochID, Kind: data.RestorePointSnapshot, Schema: observation, RecordedAt: time.Now().UTC(), Snapshot: &data.RestoreSnapshotPoint{ObjectKey: path.Join(epoch.RemotePrefix, "restore-points", pointID, "snapshot.sqlite"), SHA256: strings.Repeat("a", 64), Size: 4096}}
	source := restore.SQLiteSource{Kind: restore.SQLiteSnapshot, Snapshot: &restore.SnapshotSource{BindingID: string(epoch.BindingID), Epoch: string(epoch.EpochID), PointID: pointID, ObjectKey: point.Snapshot.ObjectKey, SHA256: point.Snapshot.SHA256, Size: point.Snapshot.Size}}
	receipt := restore.SQLiteReceipt{OperationID: operation.ID + "-" + string(db.Binding.DatabaseID), Source: source, ToolVersion: restore.SnapshotToolVersion, ObservedAt: time.Now().UTC(), Schema: restore.SQLiteSchemaObservation{State: restore.VerifiedSchema, Marker: observation.Marker, CatalogSHA256: observation.CatalogSHA256}, IntegrityCheck: "passed", ForeignKeyCheck: "passed", InvariantCheck: "passed", Coverage: &restore.CoverageEvidence{Source: source, CoveredThrough: now}, LossWindow: restore.LossWindow{State: restore.LossBounded, From: now, To: time.Now().UTC(), Reason: "Independent source-bound coverage proof bounds possible loss; later writes may be absent."}}
	receipt.LossWindow.To = receipt.ObservedAt
	coverage := []archive.Coverage{archive.SQLiteCoverageVariant(archive.SQLiteCoverage{DatabaseID: db.Binding.DatabaseID, WritersQuiescedAt: now, Observation: observation, Point: &point, Receipt: &receipt})}
	operation, err = r.store.SaveArchiveCoverage(ctx, operation, coverage)
	if err != nil {
		t.Fatal("archive coverage", err, "receipt valid", receipt.Valid(), "admits", coverage[0].Admits(archivePlan, archive.SQLiteDatabaseVariant(db), operation.ID, time.Now().UTC()))
	}
	for _, stage := range []archive.Stage{archive.ReplicaStopIntent, archive.Quiesced} {
		operation, err = r.store.AdvanceArchive(ctx, operation, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	manifest := archive.Manifest{Plan: archivePlan, OperationID: operation.ID, Coverage: coverage, QuiescedAt: time.Now().UTC(), PreparedAt: time.Now().UTC()}
	operation, err = r.store.SaveArchiveManifest(ctx, operation, manifest)
	if err != nil {
		t.Fatal(err)
	}
	disposition := archive.Disposition{}
	for _, stage := range []archive.DispositionStage{archive.CleanupIntent, archive.RuntimeRetired, archive.RenameIntent, archive.Renamed} {
		disposition, err = r.store.AdvanceArchiveDisposition(ctx, operation, disposition, archive.Disposition{ArchiveID: archivePlan.ArchiveID, Sequence: disposition.Sequence + 1, Stage: stage})
		if err != nil {
			t.Fatal(err)
		}
	}
	tree := archivePlan.Trees[0]
	if err = os.MkdirAll(filepath.Join(string(tree.Root), "archives"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(string(tree.Root), tree.SourceRelative), filepath.Join(string(tree.Root), tree.ArchiveRelative)); err != nil {
		t.Fatal(err)
	}
	committed := time.Now().UTC()
	_, err = r.store.FinishArchiveDisposition(ctx, operation, disposition, archive.Disposition{ArchiveID: archivePlan.ArchiveID, Sequence: disposition.Sequence + 1, Stage: archive.Retired, CommittedAt: committed, RetainUntil: committed.Add(30 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := r.store.InventoryState(ctx)
	if err != nil || len(inventory.Apps) != 1 || inventory.Apps[0].Status != target.Absent {
		t.Fatal("missing historical absence", inventory, err)
	}
	r.inventory.snapshot.Apps = target.Known([]target.App{{Name: request.App, Image: target.Observation[target.Image]{Status: target.Absent}, AllocatedHostPort: target.Observation[target.Port]{Status: target.Absent}, QuadletUnits: target.Known([]target.Unit{}), Secrets: target.Known([]target.Secret{})}})
	return candidate.Incarnation, release
}

func TestInitializationAfterArchiveRetirementAndRecreation(t *testing.T) {
	for _, retainHead := range []bool{false, true} {
		t.Run(fmt.Sprintf("retained-head-%t", retainHead), func(t *testing.T) {
			r, engine, request := incarnationInitializationRig(t)
			initial, err := engine.Plan(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = engine.Apply(t.Context(), request.App, initial.ID); err != nil {
				t.Fatal(err)
			}
			old, release := retireInitializedIncarnation(t, r, request)
			prepared, err := r.service.PlanDataPreparation(t.Context(), initializedApp(t, r, request))
			if err != nil {
				t.Fatal(err)
			}
			accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: prepared.PlanID, IdempotencyKey: "recreated-incarnation"}).Data.(jobs.Accepted)
			if err = r.runner.Run(t.Context(), accepted.OperationID); err != nil {
				t.Fatal(err)
			}
			fresh, err := r.store.ActiveDataIncarnation(t.Context(), request.App)
			if err != nil || fresh == old {
				t.Fatal("incarnation reused", err)
			}
			if retainHead {
				release.ID = "retained-old-release"
				if err = r.store.CommitRelease(t.Context(), request.App, release); err != nil {
					t.Fatal(err)
				}
			}
			request.FirstReleasePlan = prepared.PlanID
			_, candidateErr := r.store.InitializationCandidate(t.Context(), request.App, request.FirstReleasePlan)
			if (candidateErr != nil) != retainHead {
				t.Fatalf("retained head refused=%t: %v", retainHead, candidateErr)
			}
			planned, err := engine.Plan(t.Context(), request)
			if retainHead {
				if err == nil {
					t.Fatal("old incarnation head admitted")
				}
				return
			}
			if err != nil {
				t.Fatal("recreated initialization refused", err)
			}
			if planned.Database.IncarnationID != fresh {
				t.Fatal("initialization bound old data")
			}
			if _, err = engine.Apply(t.Context(), request.App, planned.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInitializationRechecksRetiredRuntimeBeforeApply(t *testing.T) {
	r, engine, request := incarnationInitializationRig(t)
	r.inventory.snapshot.Apps = target.Known([]target.App{{Name: request.App, Image: target.Observation[target.Image]{Status: target.Absent}, AllocatedHostPort: target.Observation[target.Port]{Status: target.Absent}, QuadletUnits: target.Known([]target.Unit{}), Secrets: target.Known([]target.Secret{})}})
	planned, err := engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	(*r.inventory.snapshot.Apps.Value)[0].Image = target.Observation[target.Image]{Status: target.Unknown}
	if _, err = engine.Apply(t.Context(), request.App, planned.ID); err == nil {
		t.Fatal("unknown runtime admitted after review")
	}
	if _, exists, err := r.store.ReadInitOperation(t.Context(), planned.ID); err != nil || exists {
		t.Fatal("refusal claimed initialization", exists, err)
	}
}
