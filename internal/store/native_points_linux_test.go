//go:build linux

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/ops"
)

func migrateNativePointFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
}

func nativeForeignRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func dropNativeCaptureMigrationFixture(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	for _, table := range []string{"data_native_archive_replays", "data_native_live_restore_resolutions", "data_native_initial_owners", "data_native_prepared_point_bundles", "data_native_recovery_inputs", "data_native_archive_tree_events", "data_native_archive_claims", "data_native_archive_verifications", "data_native_archive_dependencies", "data_native_archive_trees", "data_native_live_restore_targets", "data_native_live_restore_completion_proofs", "data_native_live_restore_target_proofs", "data_native_live_restore_events", "data_native_live_restore_claims", "data_native_live_restore_plans", "data_native_prepared_frontend_stops", "data_native_prepared_routes", "data_native_prepared_upload_intents", "data_native_backup_initialization_events", "data_native_live_restore_code_intents"} {
		if _, err := s.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatal(err)
		}
	}
	dropManagedAndPublicationMigrationFixture(t, ctx, s)
	for _, table := range []string{"data_native_repository_leases", "data_native_repository_init_events", "data_native_recovery_retentions", "data_native_recovery_deliveries", "data_native_backup_revision_events", "data_native_backup_revision_claims", "data_native_backup_revision_plans", "data_native_backup_policies", "data_native_capture_events", "data_native_capture_inputs", "data_native_checkpoints", "data_native_repository_results", "data_native_repository_intents", "data_native_verified_point_bundles", "data_native_verified_points", "data_native_point_consumers", "data_native_prepared_points", "data_native_preparation_events", "data_native_preparations", "data_native_task_inputs"} {
		if _, err := s.db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM transitions WHERE kind IN ('native_capture','native_restore_test','native_prepare')"); err != nil {
		t.Fatal(err)
	}
}

func nativePreparationFixture(t *testing.T) (*Store, *NativePreparation, *data.EngineLock, ops.Operation) {
	t.Helper()
	ctx, s := context.Background(), openTest(t)
	reserved, err := s.ReserveEngine(ctx, engineReservation(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := data.PrepareEngineLocks(ctx, s.dir, reserved.Database.BindingID); err != nil {
		t.Fatal(err)
	}
	runtime := engineRuntimeFixture(reserved)
	runtime.State, runtime.Database, runtime.Publisher, runtime.Names = data.EngineUnpublished, "", "", []string{}
	if err := s.RegisterEngineModule(ctx, reserved.Database.BindingID, runtime.Module); err != nil {
		t.Fatal(err)
	}
	saveEngineAllocationFixture(t, s, reserved)
	migrateNativePointFixture(t, s)
	requester, err := datainit.ParseRequester("deploy:" + strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	reference, err := s.SaveNativeTaskInput(ctx, NativeTaskInput{Requester: requester, Kind: ops.NativePrepare, BindingID: reserved.Database.BindingID})
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepare, App: "managed", SecretRef: reference}, requester.String(), "native-prepare")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	fence, err := s.HoldDataFence(ctx, reserved.Database.DatabaseID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitEngineRuntime(ctx, runtime, fence.ID, op.ID); err != nil {
		t.Fatal(err)
	}
	guard, err := s.AcquireEnginePreparation(ctx, reserved.Database.BindingID, fence.ID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guard.Close() })
	preparation, err := s.BeginNativePreparation(ctx, guard, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordNativeStopIntent(ctx, preparation, NativeStopIntent{Mode: "unpublished_absent", ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	lifetime, err := data.AcquireEngineLifetimeLock(ctx, s.dir, reserved.Database.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lifetime.Release() })
	return s, preparation, lifetime, op
}

func nativeAbsentStopFixture() NativeStopObservation {
	return NativeStopObservation{Mode: "unpublished_absent", FinishedAt: time.Now().UTC(), SourceAbsent: true, RestartJobsAbsent: true, IngressClosed: true}
}

func TestNativePreparationRequiresActualRetainedLocksAndKnownStop(t *testing.T) {
	s, preparation, lifetime, op := nativePreparationFixture(t)
	ctx := context.Background()
	if _, err := s.BeginNativePreparation(ctx, &EnginePreparation{}, op.ID); err == nil {
		t.Fatal("zero launch capability admitted")
	}
	again, err := s.BeginNativePreparation(ctx, preparation.preparation, op.ID)
	if err != nil || again.PointID() != preparation.PointID() {
		t.Fatal("recorded preparation not idempotent", err)
	}
	input, err := s.ReadNativeTaskInput(ctx, op.ID)
	if err != nil || input.BindingID != preparation.intent.BindingID || input.Kind != ops.NativePrepare {
		t.Fatal("protected task reference lost", err)
	}
	stop := nativeAbsentStopFixture()
	for _, mutate := range []func(*NativeStopObservation){
		func(o *NativeStopObservation) { o.Forced = true },
		func(o *NativeStopObservation) { o.OOMKilled = true },
		func(o *NativeStopObservation) { o.ExitCode = 137 },
		func(o *NativeStopObservation) { o.RestartJobsAbsent = false },
		func(o *NativeStopObservation) { o.IngressClosed = false },
		func(o *NativeStopObservation) { o.Mode = "unknown" },
	} {
		invalid := stop
		mutate(&invalid)
		if _, err := s.RecordNativeStoppedPreparation(ctx, preparation, lifetime, invalid); err == nil {
			t.Fatal("unknown or unsafe stop admitted")
		}
	}
	if _, err := s.RecordNativeStoppedPreparation(ctx, preparation, &data.EngineLock{}, stop); err == nil {
		t.Fatal("zero lifetime capability admitted")
	}
	foreignRoot := nativeForeignRoot(t)
	if err := data.PrepareEngineLocks(ctx, foreignRoot, preparation.intent.BindingID); err != nil {
		t.Fatal(err)
	}
	foreign, err := data.AcquireEngineLifetimeLock(ctx, foreignRoot, preparation.intent.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Release() }()
	if _, err := s.RecordNativeStoppedPreparation(ctx, preparation, foreign, stop); !errors.Is(err, ErrConflict) {
		t.Fatal("lifetime from another control root admitted", err)
	}
	stopped, err := s.RecordNativeStoppedPreparation(ctx, preparation, lifetime, stop)
	if err != nil || !stopped.active(s) {
		t.Fatal("actual retained locks refused", err)
	}
	if _, err := s.RecordNativeStoppedPreparation(ctx, preparation, lifetime, stop); err != nil {
		t.Fatal("identical stopped fact conflicted", err)
	}
	changed := stop
	changed.FinishedAt = changed.FinishedAt.Add(time.Second)
	if _, err := s.RecordNativeStoppedPreparation(ctx, preparation, lifetime, changed); err == nil {
		t.Fatal("immutable stopped fact rewritten")
	}
	if err := lifetime.Release(); err != nil {
		t.Fatal(err)
	}
	if stopped.active(s) {
		t.Fatal("released lifetime lock retained authority")
	}
	if _, err := s.RecordNativeStoppedPreparation(ctx, preparation, lifetime, stop); err == nil {
		t.Fatal("released lifetime lock admitted")
	}
}

// This fixture writes synthetic phase bytes directly into a private test store.
// It proves protected joins and immutability, never a native or remote restore.
func nativeCompletedPointFixture(t *testing.T) (*Store, *NativePreparation, NativePreparedPointView) {
	s, preparation, _, point := nativeCompletedPointLocksFixture(t)
	return s, preparation, point
}

func nativeCompletedPointLocksFixture(t *testing.T) (*Store, *NativePreparation, *data.EngineLock, NativePreparedPointView) {
	t.Helper()
	s, preparation, lifetime, _ := nativePreparationFixture(t)
	ctx, stop := context.Background(), nativeAbsentStopFixture()
	if _, err := s.RecordNativeStoppedPreparation(ctx, preparation, lifetime, stop); err != nil {
		t.Fatal(err)
	}
	i := preparation.intent
	now := time.Now().UTC()
	stopRaw, _ := json.Marshal(stop)
	verified := []byte(`{"fixture":"synthetic verification"}`)
	v := NativePreparedPointView{Format: nativePreparedPointFormat, PointID: i.PointID, PreparationOperationID: i.OperationID, FenceOwnerOpID: i.OperationID, FenceID: i.FenceID, BindingID: i.BindingID, EpochID: i.EpochID, DatabaseID: i.DatabaseID, IncarnationID: i.IncarnationID, Generation: i.Generation, GenerationID: strings.Repeat("e", 32), PolicySHA256: i.PolicySHA256, RuntimeVersion: i.RuntimeVersion, RuntimeSHA256: i.RuntimeSHA256, AllocationID: string(i.DatabaseID), AllocationSHA256: i.AllocationSHA256, SourceKind: i.SourceKind, SourceSHA256: strings.Repeat("1", 64), SourceSealSHA256: strings.Repeat("2", 64), SourceWitnessSHA256: strings.Repeat("3", 64), CheckpointSHA256: strings.Repeat("4", 64), StopProofSHA256: nativePointHash(stopRaw), RestoreReceiptSHA256: nativePointHash(verified), BundleManifestSHA256: strings.Repeat("5", 64), TransportPointSHA256: strings.Repeat("6", 64), ConfigSHA256: strings.Repeat("7", 64), SigningRecoverySHA256: strings.Repeat("8", 64), Names: []string{}, PreparedAt: now, ExpiresAt: now.Add(10 * time.Minute), RetainUntil: now.Add(30 * 24 * time.Hour)}
	verified, _ = json.Marshal(nativePreparedVerification{Format: "brine-native-prepared-verification-v1", PointID: v.PointID, SourceSHA256: v.SourceSHA256, SourceNativeSHA256: nativePointHash(nil), RestoredNativeSHA256: nativePointHash(nil), FinalPairsSHA256: nativePointHash(nil), ConfirmedBoundSHA256: nativePointHash(nil), ManifestSHA256: v.BundleManifestSHA256, TransportSHA256: v.TransportPointSHA256, VerifiedAt: now})
	v.RestoreReceiptSHA256 = nativePointHash(verified)
	raw, err := json.Marshal(v)
	if err != nil || !validNativePreparedPoint(v) {
		t.Fatal("invalid point fixture", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	sourceFacts, err := nativePreparedSourceFactBytes(v)
	if err != nil {
		t.Fatal(err)
	}
	remoteFacts, err := nativePreparedRemoteFactBytes(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []struct {
		stage string
		proof []byte
	}{{"source", sourceFacts}, {"remote", remoteFacts}, {"verified", verified}, {"completed", raw}} {
		if err := appendNativePreparationProof(ctx, tx, i, preparation.raw, phase.stage, phase.proof); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO data_native_prepared_points VALUES(?,?,?,?)", i.PointID, i.BindingID, i.EpochID, raw); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadNativePreparedPoint(ctx, i.PointID); err == nil {
		t.Fatal("nonterminal preparation operation supplied exact point")
	}
	if err := s.SetOperationState(ctx, i.OperationID, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	return s, preparation, lifetime, v
}

func TestNativePointReadBindsCompletionAndOriginalOwner(t *testing.T) {
	s, preparation, v := nativeCompletedPointFixture(t)
	ctx := context.Background()
	point, err := s.ReadNativePreparedPoint(ctx, v.PointID)
	if err != nil || !data.ValidCatalogHash(point.PointSHA256) || point.FenceOwnerOpID != v.PreparationOperationID || point.GenerationID == "" {
		t.Fatal("protected point join failed", err)
	}
	if _, err := s.ReadNativePreparedPoint(ctx, strings.Repeat("f", 32)); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing point gained authority", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE data_native_prepared_points SET epoch_id=? WHERE id=?", strings.Repeat("f", 32), v.PointID); err == nil {
		t.Fatal("point identity mutable")
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM data_native_preparation_events WHERE point_id=?", v.PointID); err == nil {
		t.Fatal("preparation evidence deletable")
	}
	if _, err := s.db.ExecContext(ctx, "DROP TRIGGER data_native_prepared_points_no_update"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE data_native_prepared_points SET epoch_id=? WHERE id=?", strings.Repeat("f", 32), v.PointID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadNativePreparedPoint(ctx, v.PointID); err == nil {
		t.Fatal("crossed row identity admitted")
	}
	if !preparation.preparation.Active(v.BindingID) {
		t.Fatal("read changed guard ownership")
	}
}

func TestNativePointConsumerLedgerIsAtomicAndOneUse(t *testing.T) {
	s, preparation, v := nativeCompletedPointFixture(t)
	ctx := context.Background()
	c := nativePointConsumer{kind: nativePointModulePublish, pointID: v.PointID, planID: "sha256:" + strings.Repeat("1", 64), applyOperationID: "publication-fixture", requester: "deploy:" + strings.Repeat("1", 64), binding: v.BindingID, epoch: v.EpochID, generation: v.Generation, generationID: v.GenerationID, policySHA256: v.PolicySHA256}
	raw, _ := c.canonical()
	// Future18 operation registration is outside this17 fixture. Seed only this
	// closed operation row while schema checks are disabled, then restore them.
	if _, err := s.db.ExecContext(ctx, "PRAGMA ignore_check_constraints=ON"); err != nil {
		t.Fatal(err)
	}
	now := timestamp()
	if _, err := s.db.ExecContext(ctx, "INSERT INTO operations VALUES(?,NULL,?,?,?,?,?,?,?,?,?)", c.applyOperationID, c.requester, "publication-key", ops.Preflight, now, now, "module_publish_apply", "managed", c.planID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA ignore_check_constraints=OFF"); err != nil {
		t.Fatal(err)
	}
	claim := func(commit bool, consumer nativePointConsumer) error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		_, err = s.claimNativePreparedPointTx(ctx, tx, preparation.preparation.guard, consumer)
		if err != nil || !commit {
			return err
		}
		return tx.Commit()
	}
	if err := claim(false, c); err != nil {
		t.Fatal(err)
	}
	foreignRoot := nativeForeignRoot(t)
	if err := data.PrepareEngineLocks(ctx, foreignRoot, v.BindingID); err != nil {
		t.Fatal(err)
	}
	foreign, err := data.AcquireEngineLaunchGuard(ctx, foreignRoot, v.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Release() }()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, foreignErr := s.claimNativePreparedPointTx(ctx, tx, foreign, c)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(foreignErr, ErrConflict) {
		t.Fatal("launch guard from another control root consumed point", foreignErr)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM data_native_point_consumers").Scan(&n); err != nil || n != 0 {
		t.Fatal("point claim escaped rolled-back publication transaction", err)
	}
	if err := claim(true, c); err != nil {
		t.Fatal(err)
	}
	if err := claim(true, c); err != nil {
		t.Fatal("identical consumer was not idempotent", err)
	}
	crossed := c
	crossed.applyOperationID = "different-operation"
	if err := claim(true, crossed); !errors.Is(err, ErrConflict) {
		t.Fatal("point consumed twice", err)
	}
	var saved []byte
	if err := s.db.QueryRowContext(ctx, "SELECT canonical FROM data_native_point_consumers WHERE point_id=?", v.PointID).Scan(&saved); err != nil || !bytes.Equal(saved, raw) {
		t.Fatal("first consumer overwritten", err)
	}
	if err := preparation.preparation.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "DROP TRIGGER data_native_prepared_points_no_update"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE data_native_prepared_points SET canonical=json_set(canonical,'$.expires_at',?) WHERE id=?", time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), v.PointID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseDataFence(ctx, v.FenceID, v.FenceOwnerOpID); !errors.Is(err, ErrConflict) {
		t.Fatal("generic release cleared a consumed point before transition admission", err)
	}
	if err := claim(true, c); !errors.Is(err, ErrConflict) {
		t.Fatal("released launch lock retained claim authority", err)
	}
}

func TestNativePointCodecRejectsUnknownNullAndOnlineArms(t *testing.T) {
	_, _, v := nativeCompletedPointFixture(t)
	raw, _ := json.Marshal(v)
	for _, invalid := range []string{
		strings.Replace(string(raw), `"format":`, `"unknown":true,"format":`, 1),
		strings.Replace(string(raw), `"generation":0`, `"generation":null`, 1),
		strings.Replace(string(raw), `"unpublished"`, `"online"`, 1),
		strings.Replace(string(raw), v.FenceOwnerOpID, "forged-owner", 1),
	} {
		if _, err := decodeNativePreparedPoint([]byte(invalid)); err == nil {
			t.Fatal("invalid exact point admitted")
		}
	}
}
