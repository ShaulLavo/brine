//go:build linux

package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/restic"
)

func TestNativeReactivatedCredentialsSurvivePublicationEpoch(t *testing.T) {
	f := nativeTransitionStateFixture(t, data.EngineUnpublished)
	ctx, s, c := t.Context(), f.store, f.input.Common()
	reserved, err := s.ReadReservedEngine(ctx, c.Scope.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	active, credential := seedNativeEpochBackupHistory(t, f, reserved)
	point := seedNativeEpochPreparedBundle(t, f, reserved, active)
	c.PreparedPoint.SHA256 = nativePointHash(abandonJSON(t, point))
	f.input, err = data.NewEnginePublicationTransition(c, data.EnginePublicationTarget{CandidateSHA256: strings.Repeat("8", 64), Receipt: storeTransitionEvidence(c.Scope, f.runtime.EpochID, data.EnginePublicationEvidence, 8)})
	if err != nil {
		t.Fatal(err)
	}
	archive := reserved.Archive
	archive.EpochID = f.runtime.EpochID
	archive.RemotePrefix, err = data.RemotePrefix(archive.Destination.BasePrefix, c.Scope.IncarnationID, c.Scope.DatabaseID, archive.EpochID)
	if err != nil {
		t.Fatal(err)
	}
	consumer := nativePointConsumer{kind: nativePointModulePublish, pointID: point.PointID, planID: c.Scope.PlanID, applyOperationID: c.Scope.ApplyOperationID, requester: c.Scope.Requester, binding: c.Scope.BindingID, epoch: point.EpochID, generation: point.Generation, generationID: point.GenerationID, policySHA256: active.PolicySHA256}
	consumerRaw, err := consumer.canonical()
	if err != nil {
		t.Fatal(err)
	}
	abandonExec(t, s.db, "INSERT INTO data_native_point_consumers VALUES(?,?,?,?,?,?,?,?,?,?,?)", point.PointID, consumer.kind, consumer.planID, consumer.applyOperationID, consumer.requester, consumer.binding, consumer.epoch, consumer.generation, consumer.generationID, consumer.policySHA256, consumerRaw)
	record := nativeRepositoryIntentRecord{Format: "brine-native-repository-intent-v1", ID: strings.Repeat("6", 32), OperationID: c.Scope.ApplyOperationID, PlanID: c.Scope.PlanID, ConsumerSHA256: nativePointHash(consumerRaw), PointID: point.PointID, BindingID: point.BindingID, SourceEpochID: point.EpochID, EpochID: archive.EpochID, DatabaseID: point.DatabaseID, IncarnationID: point.IncarnationID, FenceID: point.FenceID, FenceOwnerOpID: point.FenceOwnerOpID, RuntimeVersion: point.RuntimeVersion, RuntimeSHA256: point.RuntimeSHA256, PolicySHA256: active.PolicySHA256, Archive: archive, ReservedAt: time.Now().UTC()}
	intent := nativeRepositoryIntentValue(record, abandonJSON(t, record))
	abandonExec(t, s.db, "INSERT INTO data_native_repository_intents VALUES(?,?,?,?,?,?,?,?)", record.ID, record.OperationID, record.BindingID, record.EpochID, archive.Destination.Endpoint, archive.Destination.Bucket, archive.RemotePrefix, intent.raw)
	seedNativeEpochRepositoryResult(t, s, intent)
	activated := activateNativeFixture(t, f)
	finish := advanceStoreTransition(t, f, activated, data.EngineTransitionFinishIntent, data.EngineLayoutPromoted)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.finishEngineTransitionTx(ctx, tx, f.preparation, finish, storeTransitionEvidence(c.Scope, f.runtime.EpochID, data.EngineCompletionEvidence, 9), false); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	permit, err := s.ReadEnginePermit(ctx, active.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	material, err := readNativeCurrentRepositoryMaterial(ctx, s.db, permit)
	if err != nil || material.CredentialRecord != credential || material.Scope.EpochID != string(f.runtime.EpochID) || material.PolicySHA256 != active.PolicySHA256 {
		t.Fatal("next publication epoch lost reused credential provenance or active policy", err)
	}
	if err := f.preparation.Close(); err != nil {
		t.Fatal(err)
	}
	assertNativeEpochCaptureAdmission(t, s, active, credential)
	status, err := s.NativeBackupStatus(ctx, "managed", string(reserved.Database.Name))
	if err != nil || status.EpochID != string(f.runtime.EpochID) || status.CredentialExpired {
		t.Fatal("next publication epoch refuses backup material", err)
	}
}

func seedNativeEpochBackupHistory(t *testing.T, f nativeTransitionFixture, reserved ReservedEngine) (NativeBackupPolicy, CredentialRecord) {
	t.Helper()
	ctx, s, c := t.Context(), f.store, f.input.Common()
	oldPolicy := strings.Repeat("9", 64)
	hash, id := strings.Repeat("a", 64), strings.Repeat("b", 32)
	credential := CredentialRecord{ID: id, Kind: "receipt", App: "managed", Requester: c.Scope.Requester, TargetHash: c.Scope.TargetHash, PolicyHash: "sha256:" + oldPolicy, BindingID: c.Scope.BindingID, EpochID: reserved.Archive.EpochID, Destination: reserved.Archive.Destination.Reference, CredentialRef: reserved.Archive.Destination.CredentialRef, Version: 1, ReceivedAt: time.Now().UTC()}
	provenance := backupcredentials.Plan{Requester: credential.Requester, Kind: backupcredentials.Kind, Version: 1, Scope: backupcredentials.Scope{App: credential.App, TargetHash: credential.TargetHash, PolicyHash: credential.PolicyHash, Binding: string(credential.BindingID), Epoch: string(credential.EpochID), Destination: string(credential.Destination), CredentialRef: credential.CredentialRef}}
	credential.PlanID = "sha256:" + nativePointHash(abandonJSON(t, provenance))
	credential.PlanHash = credential.PlanID
	abandonExec(t, s.db, "INSERT INTO data_credential_records VALUES(?,?,?,?)", credential.ID, credential.BindingID, credential.Kind, abandonJSON(t, credential))
	recoveryOp, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativeRecoverySet, App: "managed", SecretRef: id}, credential.Requester, "epoch-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, recoveryOp.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	recovery := nativeRecoveryDeliveryRecord{Format: "brine-native-recovery-delivery-v1", ID: id, OperationID: recoveryOp.ID, Requester: credential.Requester, BindingID: credential.BindingID, EpochID: credential.EpochID, Reference: "recovery", Version: 1, SHA256: hash, ReceivedAt: time.Now().UTC()}
	abandonExec(t, s.db, "INSERT INTO data_native_recovery_deliveries VALUES(?,?,?,?,?,?,?,?)", recovery.ID, recovery.OperationID, recovery.Requester, recovery.BindingID, recovery.EpochID, recovery.Reference, recovery.Version, abandonJSON(t, recovery))
	r := nativecapture.RecoveryReceipt{Kind: string(ops.NativeRecoverySet), OperationID: recovery.OperationID, BindingID: string(recovery.BindingID), EpochID: string(recovery.EpochID), Reference: recovery.Reference, Version: recovery.Version, SHA256: hash, Outcome: "stored"}
	if err := s.CompleteTask(ctx, recoveryOp.ID, ops.Succeeded, ops.TaskOutcome{Receipt: abandonJSON(t, r)}); err != nil {
		t.Fatal(err)
	}
	active := NativeBackupPolicy{Format: "brine-native-backup-policy-v1", BindingID: credential.BindingID, EpochID: credential.EpochID, RevisionPlanID: id, DeploymentPlanID: c.Scope.PlanID, DesiredSHA256: strings.TrimPrefix(c.Scope.DesiredHash, "sha256:"), RepositoryID: hash, CredentialReference: credential.CredentialRef, CredentialVersion: 1, CredentialReceiptID: id, RecoveryReference: "recovery", RecoveryVersion: 1, RecoveryDeliveryID: id, RecoverySHA256: hash, Backup: policy.SpacetimePolicy{LicenseUse: "fixture", LicenseAuthority: "fixture-v1", CaptureIntervalSeconds: 30, VerificationIntervalSeconds: 60, MaxRemoteLossSeconds: 60, MaxMaintenanceSeconds: 300}, UnitSHA256: hash, TimerSHA256: hash, ActivatedAt: time.Now().UTC()}
	for _, policyHash := range []string{oldPolicy, strings.TrimPrefix(c.Scope.PolicyHash, "sha256:")} {
		active.Revision++
		active.PolicySHA256 = policyHash
		op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativeBackupActivate, App: "managed", SecretRef: id}, credential.Requester, "epoch-activation-"+policyHash)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
			t.Fatal(err)
		}
		active.OperationID = op.ID
		raw := abandonJSON(t, active)
		abandonExec(t, s.db, "INSERT INTO data_native_backup_policies VALUES(?,?,?,?)", active.BindingID, active.Revision, active.EpochID, raw)
		r := nativecapture.BackupPolicyReceipt{Kind: string(ops.NativeBackupActivate), OperationID: op.ID, BindingID: string(active.BindingID), EpochID: string(active.EpochID), PlanID: id, Revision: active.Revision, RepositoryID: hash, PolicySHA256: nativePointHash(raw), Outcome: "active"}
		if err := s.CompleteTask(ctx, op.ID, ops.Succeeded, ops.TaskOutcome{Receipt: abandonJSON(t, r)}); err != nil {
			t.Fatal(err)
		}
	}
	return active, credential
}

func seedNativeEpochPreparedBundle(t *testing.T, f nativeTransitionFixture, reserved ReservedEngine, active NativeBackupPolicy) NativePreparedPointView {
	t.Helper()
	s, c, ctx := f.store, f.input.Common(), t.Context()
	point, source, manifest, transportRaw := nativeEmptyBundleRecordsFor(t, string(active.BindingID), string(active.EpochID), c.PreparedPoint.ID)
	var transport restic.TransportPoint
	if err := json.Unmarshal(transportRaw, &transport); err != nil {
		t.Fatal(err)
	}
	transport.Scope.RepositoryID, transport.Scope.DestinationSHA256 = active.RepositoryID, nativeArchiveLocation(reserved.Archive).SHA256()
	transportRaw = abandonJSON(t, transport)
	now := time.Now().UTC()
	initial, _ := c.Base.Unpublished()
	i := nativePreparationIntent{PointID: point.view.PointID, OperationID: c.FenceOwnerOperationID, BindingID: active.BindingID, EpochID: active.EpochID, DatabaseID: c.Scope.DatabaseID, IncarnationID: c.Scope.IncarnationID, Generation: c.Scope.Generation, RuntimeVersion: c.Base.Head().Version, RuntimeSHA256: c.Base.Head().RuntimeSHA256, AllocationSHA256: initial.Allocation.SHA256, PolicySHA256: active.PolicySHA256, SourceKind: "unpublished", FenceID: c.FenceID, CreatedAt: now}
	v := point.view
	v.Format, v.PreparationOperationID, v.FenceOwnerOpID, v.FenceID = nativePreparedPointFormat, i.OperationID, i.OperationID, i.FenceID
	v.DatabaseID, v.IncarnationID, v.Generation, v.PolicySHA256 = i.DatabaseID, i.IncarnationID, i.Generation, i.PolicySHA256
	v.RuntimeVersion, v.RuntimeSHA256, v.AllocationID, v.AllocationSHA256 = i.RuntimeVersion, i.RuntimeSHA256, string(i.DatabaseID), i.AllocationSHA256
	v.SourceSealSHA256, v.SourceWitnessSHA256, v.TransportPointSHA256 = strings.Repeat("2", 64), nativePointHash(nil), nativePointHash(transportRaw)
	v.Names, v.PreparedAt, v.ExpiresAt, v.RetainUntil = []string{}, now, now.Add(10*time.Minute), now.Add(31*24*time.Hour)
	stop := NativeStopObservation{Mode: "unpublished_absent", FinishedAt: now, SourceAbsent: true, RestartJobsAbsent: true, IngressClosed: true}
	proof := nativePreparedVerification{Format: "brine-native-prepared-verification-v1", PointID: v.PointID, SourceSHA256: v.SourceSHA256, SourceNativeSHA256: nativePointHash(nil), RestoredNativeSHA256: nativePointHash(nil), FinalPairsSHA256: nativePointHash(nil), ConfirmedBoundSHA256: nativePointHash(nil), ManifestSHA256: v.BundleManifestSHA256, TransportSHA256: v.TransportPointSHA256, VerifiedAt: now}
	v.StopProofSHA256, v.RestoreReceiptSHA256 = nativePointHash(abandonJSON(t, stop)), nativePointHash(abandonJSON(t, proof))
	abandonExec(t, s.db, "INSERT INTO operations(id,requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref) VALUES(?,?,?,?,?,?,?,?,?)", i.OperationID, c.Scope.Requester, "epoch-preparation", "succeeded", timestamp(), timestamp(), string(ops.NativePrepare), "managed", strings.Repeat("1", 32))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	intentRaw := abandonJSON(t, i)
	abandonExec(t, tx, "INSERT INTO data_native_preparations VALUES(?,?,?,?,?,?)", i.PointID, i.OperationID, i.BindingID, i.EpochID, i.FenceID, intentRaw)
	sourceFact, err := nativePreparedSourceFactBytes(v)
	if err != nil {
		t.Fatal(err)
	}
	remoteFact, err := nativePreparedRemoteFactBytes(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []struct {
		name string
		raw  []byte
	}{{"stop_intent", abandonJSON(t, NativeStopIntent{Mode: "unpublished_absent", ObservedAt: now})}, {"stopped", abandonJSON(t, stop)}, {"source", sourceFact}, {"remote", remoteFact}, {"verified", abandonJSON(t, proof)}, {"completed", abandonJSON(t, v)}} {
		if err := appendNativePreparationProof(ctx, tx, i, intentRaw, stage.name, stage.raw); err != nil {
			t.Fatal(err)
		}
	}
	abandonExec(t, tx, "INSERT INTO data_native_prepared_points VALUES(?,?,?,?)", v.PointID, v.BindingID, v.EpochID, abandonJSON(t, v))
	if err := insertNativePreparedBundle(ctx, tx, v.PointID, source, manifest, transportRaw, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return v
}

func seedNativeEpochRepositoryResult(t *testing.T, s *Store, intent nativeRepositoryIntent) {
	t.Helper()
	r := intent.record
	now := time.Now().UTC()
	lease := nativeRepositoryLeaseRecord{Format: "brine-native-repository-lease-v1", ID: strings.Repeat("7", 32), OperationID: r.OperationID, BindingID: r.BindingID, EpochID: r.EpochID, IntentID: r.ID, IntentSHA256: intent.hash, ConsumerSHA256: r.ConsumerSHA256, CreatedAt: now}
	leaseRaw := abandonJSON(t, lease)
	abandonExec(t, s.db, "INSERT INTO data_native_repository_leases VALUES(?,?,?,?,'released',?)", lease.ID, lease.OperationID, lease.BindingID, lease.EpochID, leaseRaw)
	previous := intent.hash
	for n, stage := range []string{"init_issued", "initialized"} {
		e := nativeRepositoryInitEvent{IntentID: r.ID, IntentSHA256: intent.hash, LeaseSHA256: nativePointHash(leaseRaw), Sequence: uint64(n + 1), Stage: stage, PreviousSHA256: previous, ObservedAt: now}
		raw := abandonJSON(t, e)
		abandonExec(t, s.db, "INSERT INTO data_native_repository_init_events VALUES(?,?,?,?,?)", r.ID, e.Sequence, stage, previous, raw)
		previous = nativePointHash(raw)
	}
	result := NativeInitializedRepositoryResult{IntentID: r.ID, IntentSHA256: intent.hash, RepositoryID: strings.Repeat("a", 64), ConfigSHA256: strings.Repeat("c", 64), Scope: restic.Scope{BindingID: string(r.BindingID), EpochID: string(r.EpochID), RepositoryID: strings.Repeat("a", 64), DestinationSHA256: nativeArchiveLocation(intent.archive).SHA256(), CredentialVersion: 1, RecoveryVersion: 1, ExpiresAt: now.Add(10 * time.Minute)}, VerifiedAt: now}
	abandonExec(t, s.db, "INSERT INTO data_native_repository_results VALUES(?,?,?)", r.ID, result.RepositoryID, abandonJSON(t, result))
}

func assertNativeEpochCaptureAdmission(t *testing.T, s *Store, active NativeBackupPolicy, credential CredentialRecord) {
	t.Helper()
	ctx := t.Context()
	requester, err := datainit.ParseRequester(credential.Requester)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.SaveNativeTaskInput(ctx, NativeTaskInput{Requester: requester, Kind: ops.NativeCapture, BindingID: active.BindingID})
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativeCapture, App: credential.App, SecretRef: ref}, credential.Requester, "capture-next-epoch")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	capture, err := s.BeginNativeOnlineCapture(ctx, op.ID)
	if err != nil {
		t.Fatal("capture admission after publication", err)
	}
	defer func() { _ = capture.Close() }()
	material, err := capture.Material(ctx)
	if err != nil || material.CredentialRecord != credential || material.PolicySHA256 != active.PolicySHA256 {
		t.Fatal("capture cannot inherit reactivated material after publication", err)
	}
}
