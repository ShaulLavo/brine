//go:build linux

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/target"
)

type nativeAbandonFixture struct {
	state     *Store
	operation ops.Operation
	point     NativePreparedPointView
	stop      NativeStopObservation
	desired   ManagedDesiredState
}

func abandonJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func abandonExec(t *testing.T, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, query string, args ...any) {
	t.Helper()
	if _, err := q.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

// This fixture seeds synthetic passive evidence into a private Store. Its
// module bytes and descriptor are real, while no host or remote tool runs.
func newNativeAbandonFixture(t *testing.T) nativeAbandonFixture {
	t.Helper()
	ctx := t.Context()
	artifact, err := os.ReadFile("../spacetimedb/testdata/recovery-v1.wasm")
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := os.ReadFile("../spacetimedb/testdata/descriptor-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	wasm, wire, err := spacetimedb.ModuleIdentities(artifact)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := spacetimedb.DecodeDescriptorV10(descriptor, spacetimedb.DefaultSchemaLimits())
	if err != nil {
		t.Fatal(err)
	}
	module := data.EngineModuleDefinition{Program: data.NativeProgramWireHash(wire), Wasm: data.WasmSHA256(wasm), Schema: data.EngineSchemaDefinition{Marker: "game-v1", Canonicalizer: schema.Version(), SHA256: schema.SHA256()}}
	s, p, deploy := managedControlStateFixtureAtRoot(t, false, false, "", module)
	wanted := commitAbandonFixtureRelease(t, s, p, deploy)
	requester, err := datainit.ParseRequester("deploy:" + strings.Repeat("4", 64))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.SaveNativeTaskInput(ctx, NativeTaskInput{Requester: requester, Kind: ops.NativePrepare, BindingID: wanted.BindingID})
	if err != nil {
		t.Fatal(err)
	}
	prepare, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepare, App: p.App, SecretRef: ref}, requester.String(), "abandon-prepare")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, prepare.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	fence, err := s.HoldDataFence(ctx, p.Managed.Database.DatabaseID, prepare.ID)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := s.AcquireEnginePreparation(ctx, wanted.BindingID, fence.ID, prepare.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guard.Close() }()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	intent, err := buildNativePreparationIntent(ctx, tx, prepare, wanted.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	intent.PointID, err = data.NewID()
	if err != nil {
		t.Fatal(err)
	}
	intent.CreatedAt = time.Now().UTC().Add(-20 * time.Minute)
	intentRaw := abandonJSON(t, intent)
	abandonExec(t, tx, "INSERT INTO data_native_preparations VALUES(?,?,?,?,?,?)", intent.PointID, intent.OperationID, intent.BindingID, intent.EpochID, intent.FenceID, intentRaw)
	permit, err := readEnginePermit(ctx, tx, wanted.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, point, verification := abandonFixtureCheckpoint(t, intent, permit.Runtime, artifact, descriptor)
	stop := NativeStopObservation{Mode: "clean_exit", ContainerID: strings.Repeat("1", 64), FinishedAt: intent.CreatedAt.Add(time.Second), SourceAbsent: true, RestartJobsAbsent: true, IngressClosed: true}
	point.StopProofSHA256 = nativePointHash(abandonJSON(t, stop))
	point.RestoreReceiptSHA256 = nativePointHash(abandonJSON(t, verification))
	seedAbandonFixturePoint(t, tx, intent, intentRaw, point, stop, verification, checkpoint, artifact, descriptor, p.App)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	receipt := nativecapture.TaskReceipt{Kind: string(ops.NativePrepare), OperationID: prepare.ID, BindingID: string(point.BindingID), EpochID: string(point.EpochID), PointID: point.PointID, PointSHA256: nativePointHash(abandonJSON(t, point)), Outcome: "verified", Engine: "spacetimedb", ModuleSHA256: point.ArtifactWasmSHA256, SchemaSHA256: point.SchemaSHA256, CaptureIntervalSeconds: 30, Loss: "unknown"}
	if err := s.CompleteTask(ctx, prepare.ID, ops.Succeeded, ops.TaskOutcome{Receipt: abandonJSON(t, receipt)}); err != nil {
		t.Fatal(err)
	}
	ref, err = s.SaveNativeTaskInput(ctx, NativeTaskInput{Requester: requester, Kind: ops.NativePrepareAbandon, BindingID: point.BindingID, PointID: point.PointID})
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepareAbandon, App: p.App, SecretRef: ref}, requester.String(), "abandon")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	return nativeAbandonFixture{state: s, operation: op, point: point, stop: stop, desired: wanted}
}

func commitAbandonFixtureRelease(t *testing.T, s *Store, p plan.Plan, op ops.Operation) ManagedDesiredState {
	t.Helper()
	ctx := t.Context()
	wanted := ManagedDesiredState{BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, Revision: 1, PlanID: p.Hash, OperationID: op.ID, EngineRunning: true, IngressServing: true}
	if _, err := s.CommitManagedDesired(ctx, op.ID, 0, wanted); err != nil {
		t.Fatal(err)
	}
	unit := target.Unit{Name: "brine-engine-" + string(wanted.BindingID) + ".service", Hash: "sha256:" + p.Managed.EngineUnitHash}
	release := Release{ID: op.ID, PlanID: p.Hash, Image: p.Image, HostPort: p.HostPort, Secrets: []plan.SecretBinding{}, Units: []target.Unit{unit}, CaddyFile: target.CaddyFile{Name: p.App + ".caddy", Hash: "sha256:" + strings.Repeat("a", 64)}, CaddyGeneration: 1, Managed: &ops.ManagedRelease{BindingID: wanted.BindingID, EpochID: wanted.EpochID, RuntimeSHA256: wanted.RuntimeSHA256, EngineUnit: unit, EnginePort: p.Managed.EnginePort}}
	route := ManagedRouteState{App: p.App, BindingID: wanted.BindingID, EpochID: wanted.EpochID, RuntimeSHA256: wanted.RuntimeSHA256, DesiredRevision: 1, PlanID: p.Hash, OperationID: op.ID, Revision: 1, Generation: 1, Present: true, FileHash: release.CaddyFile.Hash, RootSHA256: strings.Repeat("b", 64), LoadedSHA256: strings.Repeat("c", 64)}
	if _, err := s.CommitManagedRouteState(ctx, 0, route); err != nil {
		t.Fatal(err)
	}
	for _, state := range []ops.State{ops.Starting, ops.Checking, ops.Committing} {
		if err := s.SetOperationState(ctx, op.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CommitRelease(ctx, p.App, release); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, op.ID, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	return wanted
}

func abandonFixtureCheckpoint(t *testing.T, i nativePreparationIntent, runtime data.EngineRuntime, artifact, descriptor []byte) (nativecapture.Checkpoint, NativePreparedPointView, nativePreparedVerification) {
	t.Helper()
	signing := runtime.Secrets.SigningPrivate
	checkpoint, err := nativecapture.NewCheckpoint(nativecapture.CheckpointRecord{Format: nativecapture.Format, Scope: nativecapture.CheckpointScope{BindingID: string(i.BindingID), EpochID: string(i.EpochID), ReplicaID: 1}, GenerationID: strings.Repeat("e", 32), DatabaseIdentity: string(runtime.Database), PublisherIdentity: string(runtime.Publisher), Names: runtime.Names, RuntimeModuleHash: string(runtime.Module.Program), ArtifactSHA256: string(runtime.Module.Wasm), SchemaVersion: runtime.Module.Schema.Canonicalizer, SchemaSHA256: runtime.Module.Schema.SHA256, ConfigurationSHA256: strings.Repeat("7", 64), SigningRecovery: nativecapture.SigningRecovery{Kind: "external_immutable", Reference: signing.Reference, Version: signing.Version, SHA256: signing.SHA256}, Members: []nativecapture.CheckpointMember{{Path: "config.toml", Bytes: 10, SHA256: strings.Repeat("7", 64)}}}, nativecapture.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	c := checkpoint.Record()
	now := i.CreatedAt.Add(time.Minute)
	p := NativePreparedPointView{Format: nativePreparedPointFormat, PointID: i.PointID, PreparationOperationID: i.OperationID, FenceOwnerOpID: i.FenceOwnerOpID, FenceID: i.FenceID, BindingID: i.BindingID, EpochID: i.EpochID, DatabaseID: i.DatabaseID, IncarnationID: i.IncarnationID, Generation: i.Generation, GenerationID: c.GenerationID, PolicySHA256: i.PolicySHA256, RuntimeVersion: i.RuntimeVersion, RuntimeSHA256: i.RuntimeSHA256, AllocationID: string(i.DatabaseID), AllocationSHA256: i.AllocationSHA256, SourceKind: "initialized", CommittedOffset: 8, CommittedOffsetKnown: true, SourceSHA256: strings.Repeat("1", 64), SourceSealSHA256: strings.Repeat("2", 64), SourceWitnessSHA256: strings.Repeat("3", 64), CheckpointSHA256: checkpoint.SHA256(), BundleManifestSHA256: strings.Repeat("5", 64), TransportPointSHA256: strings.Repeat("6", 64), ConfigSHA256: c.ConfigurationSHA256, SigningRecoverySHA256: nativeSigningRecoverySHA256(c.SigningRecovery), NativeDatabase: c.DatabaseIdentity, NativePublisher: c.PublisherIdentity, ProgramWireHash: c.RuntimeModuleHash, ArtifactWasmSHA256: c.ArtifactSHA256, SchemaSHA256: c.SchemaSHA256, Names: c.Names, PreparedAt: now, ExpiresAt: now.Add(10 * time.Minute), RetainUntil: now.Add(31 * 24 * time.Hour)}
	proof := nativePreparedVerification{Format: "brine-native-prepared-verification-v1", PointID: p.PointID, SourceSHA256: p.SourceSHA256, SourceNativeSHA256: p.SourceWitnessSHA256, RestoredNativeSHA256: p.SourceWitnessSHA256, FinalPairsSHA256: strings.Repeat("4", 64), ConfirmedBoundSHA256: strings.Repeat("4", 64), ManifestSHA256: p.BundleManifestSHA256, TransportSHA256: p.TransportPointSHA256, ReaderNonce: strings.Repeat("1", 32), ReaderViewSHA256: strings.Repeat("2", 64), ReaderImageID: strings.Repeat("3", 64), ReaderDisposedAt: now.Add(-time.Second), VerifiedAt: now}
	return checkpoint, p, proof
}

func seedAbandonFixturePoint(t *testing.T, tx *sql.Tx, i nativePreparationIntent, intentRaw []byte, p NativePreparedPointView, stop NativeStopObservation, proof nativePreparedVerification, c nativecapture.Checkpoint, artifact, descriptor []byte, app string) {
	t.Helper()
	if !validNativePreparedPoint(p) {
		t.Fatal("invalid initialized point fixture")
	}
	source, err := nativePreparedSourceFactBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := nativePreparedRemoteFactBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	stages := []struct {
		name string
		raw  []byte
	}{{"stop_intent", abandonJSON(t, NativeStopIntent{Mode: "managed_clean_stop", ContainerID: stop.ContainerID, PID: 1, StartTicks: 1, ObservedAt: i.CreatedAt})}, {"stopped", abandonJSON(t, stop)}, {"source", source}, {"remote", remote}, {"verified", abandonJSON(t, proof)}, {"completed", abandonJSON(t, p)}}
	for _, stage := range stages {
		if err := appendNativePreparationProof(t.Context(), tx, i, intentRaw, stage.name, stage.raw); err != nil {
			t.Fatal(err)
		}
	}
	provenance := nativeCheckpointProvenance{Format: "brine-native-checkpoint-provenance-v1", PointID: p.PointID, BindingID: p.BindingID, EpochID: p.EpochID, GenerationID: p.GenerationID, CheckpointSHA256: c.SHA256(), SourceSHA256: p.SourceSHA256, SourceEvidenceSHA256: p.SourceWitnessSHA256, ArtifactSHA256: p.ArtifactWasmSHA256, ProgramWireHash: p.ProgramWireHash, DescriptorSHA256: nativePointHash(descriptor), SchemaSHA256: p.SchemaSHA256, ReaderDisposalSHA256: nativePointHash([]byte(proof.ReaderNonce + proof.ReaderViewSHA256 + proof.ReaderImageID)), ArtifactProofSHA256: ""}
	abandonExec(t, tx, "INSERT INTO data_native_checkpoints VALUES(?,?,?,?,?,?,?,?,?)", p.PointID, p.BindingID, p.EpochID, p.GenerationID, i.OperationID, c.Bytes(), abandonJSON(t, provenance), artifact, descriptor)
	abandonExec(t, tx, "INSERT INTO data_native_prepared_points VALUES(?,?,?,?)", p.PointID, p.BindingID, p.EpochID, abandonJSON(t, p))
	routes := NativePreparedRoutes{PointID: p.PointID, OperationID: i.OperationID, App: app, PreviousGeneration: 1, PreviousFiles: map[string]string{}, CurrentGeneration: 2, CurrentFiles: map[string]string{}, RecordedAt: p.PreparedAt}
	abandonExec(t, tx, "INSERT INTO data_native_prepared_routes VALUES(?,?,?)", p.PointID, "withdrawn", abandonJSON(t, routes))
}

func (f nativeAbandonFixture) open(t *testing.T) (*NativeAbandonment, NativeAbandonObservation) {
	t.Helper()

	cap, err := f.state.OpenNativeAbandonment(t.Context(), f.operation.ID)
	if err != nil {
		t.Fatal("open initialized abandonment", err)
	}
	t.Cleanup(func() { _ = cap.Close() })
	expected, err := cap.Expected(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return cap, NativeAbandonObservation{Stop: f.stop, SourceSHA256: f.point.SourceSealSHA256, CodeSHA256: expected.CodeSHA256, RoutesSHA256: cap.facts.routesSHA, Directory: data.EngineDirectoryIdentity{Device: expected.Permit.Allocation.DirectoryDevice, Inode: expected.Permit.Allocation.DirectoryInode}, CheckpointSHA: f.point.CheckpointSHA256, ObservedAt: time.Now().UTC()}
}

func TestNativeAbandonPositive(t *testing.T) {
	fixture := newNativeAbandonFixture(t)
	cap, observed := fixture.open(t)
	if _, err := fixture.state.CompleteNativeAbandonment(t.Context(), cap, observed); err != nil {
		t.Fatal("complete initialized abandonment", err)
	}
	outcome, err := fixture.state.ReadNativeAbandonmentOutcome(t.Context(), fixture.operation.ID)
	if err != nil || outcome == nil {
		t.Fatal("read committed abandonment", err)
	}
	current, err := fixture.state.ReadManagedDesired(t.Context(), fixture.point.BindingID)
	if err != nil || current.Revision != 2 || current.EngineRunning || current.IngressServing {
		t.Fatal("stopped revision missing", current, err)
	}
	future := current
	future.Revision = 3
	future.OperationID = "future-lifecycle"
	raw := abandonJSON(t, future)
	abandonExec(t, fixture.state.db, "INSERT INTO operations(id,plan_id,requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of) VALUES(?,?,?,?,?,?,?,?,?,?,?)", future.OperationID, future.PlanID, "future", "future", "succeeded", timestamp(), timestamp(), string(ops.Deploy), fixture.operation.App, "", "")
	abandonExec(t, fixture.state.db, "INSERT INTO data_engine_desired_states VALUES(?,?,?,?,?)", future.BindingID, future.Revision, future.PlanID, future.OperationID, raw)
	abandonExec(t, fixture.state.db, "UPDATE data_engine_desired_heads SET revision=? WHERE binding_id=?", future.Revision, future.BindingID)
	if _, err := fixture.state.ReadNativeAbandonmentOutcome(t.Context(), fixture.operation.ID); err != nil {
		t.Fatal("historical stopped receipt invalidated by later head", err)
	}
}

func TestNativeAbandonCompleteRollsBackFenceFailure(t *testing.T) {
	fixture := newNativeAbandonFixture(t)
	cap, observed := fixture.open(t)
	trigger := "fixture_abandon_reject_" + fixture.operation.ID
	abandonExec(t, fixture.state.db, "CREATE TRIGGER "+trigger+" BEFORE UPDATE OF state ON data_fences BEGIN SELECT RAISE(ABORT,'fixture fence failure'); END")
	t.Cleanup(func() { _, _ = fixture.state.db.Exec("DROP TRIGGER IF EXISTS " + trigger) })
	if _, err := fixture.state.CompleteNativeAbandonment(t.Context(), cap, observed); err == nil {
		t.Fatal("injected fence failure committed")
	}
	var state string
	if err := fixture.state.db.QueryRowContext(t.Context(), "SELECT state FROM operations WHERE id=?", fixture.operation.ID).Scan(&state); err != nil || state != string(ops.Preflight) {
		t.Fatal("operation advanced after rollback", state, err)
	}
	var fenceState string
	if err := fixture.state.db.QueryRowContext(t.Context(), "SELECT state FROM data_fences WHERE operation_id=?", fixture.point.FenceOwnerOpID).Scan(&fenceState); err != nil || fenceState != string(data.FenceHeld) {
		t.Fatal("fence changed after rollback", fenceState, err)
	}
	current, err := fixture.state.ReadManagedDesired(t.Context(), fixture.point.BindingID)
	if err != nil || current.Revision != 1 || !current.EngineRunning || !current.IngressServing {
		t.Fatal("desired state changed after rollback", current, err)
	}
}
