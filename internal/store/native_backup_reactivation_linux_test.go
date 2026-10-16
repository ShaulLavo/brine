//go:build linux

package store

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestNativeBackupReactivationOnInitializedEngine(t *testing.T) {
	t.Run("normal", func(t *testing.T) { checkNativeBackupReactivation(t, "") })
	t.Run("before_units", func(t *testing.T) { checkNativeBackupReactivation(t, "prepared") })
	t.Run("units_issued", func(t *testing.T) { checkNativeBackupReactivation(t, "units_issued") })
}

func checkNativeBackupReactivation(t *testing.T, interruption string) {
	t.Helper()
	s, original := nativeReactivationControlFixture(t)
	ctx := t.Context()
	_, desired, err := s.LoadPlan(ctx, original.Hash)
	if err != nil {
		t.Fatal(err)
	}
	d := desired.BackupDestinations[0]
	desired.BackupRetention = []data.RetentionEvidence{{Destination: d.Reference, Endpoint: d.Endpoint, Bucket: d.Bucket, BasePrefix: d.BasePrefix, VerificationID: strings.Repeat("f", 32), VerifiedAt: time.Now().UTC().Format(time.RFC3339Nano), FreshnessSeconds: 3600, NoObjectExpiration: true}}
	in := nativeReactivationInput(t, s, desired)
	original = saveNativeReactivationDeployment(t, s, in)
	deploy, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: original.Hash}, "requester", "initial-release")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []ops.State{ops.Preflight, ops.Preparing} {
		if err := s.SetOperationState(ctx, deploy.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	commitAbandonFixtureRelease(t, s, original, deploy)
	requester := "deploy:" + strings.Repeat("c", 64)
	previous := seedNativeReactivationPolicy(t, s, original, desired, requester)
	before, err := s.ReadEnginePermit(ctx, previous.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../policy/testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	current, err := policy.Parse([]byte("allow_live_restore = true\n" + string(raw) + "\n[[allowed_registries]]\nhost=\"ghcr.io\"\nrepository_prefixes=[\"shaullavo\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	desired.PolicyHash = current.Hash()
	in.Desired = desired
	deployment := saveNativeReactivationDeployment(t, s, in)
	review, err := s.SaveNativeBackupRevisionPlan(ctx, deployment.Hash, requester)
	if err != nil {
		t.Fatal("policy-only reactivation refused on initialized engine", err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativeBackupActivate, App: review.App, SecretRef: review.ID}, requester, "reactivation")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	activation, err := s.ClaimNativeBackupActivation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = activation.Close() })
	metadata, err := activation.Metadata(ctx)
	if err != nil || metadata.Material.PolicySHA256 != strings.TrimPrefix(current.Hash(), "sha256:") || metadata.Material.CredentialRecord.PolicyHash == current.Hash() {
		t.Fatal("reactivation must reuse the original credential provenance", err)
	}
	if interruption != "prepared" {
		if err := activation.BeginUnits(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var inspection *NativeBackupInspection
	if interruption != "" {
		inspection = resumeNativeReactivation(t, s, op, activation)
		activation, err = inspection.Activation(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	if interruption == "prepared" {
		if err := inspection.BeginResolutionUnits(ctx); err != nil {
			t.Fatal(err)
		}
		if err := activation.BeginUnits(ctx); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := activation.Commit(ctx, NativeBackupActivationObservation{RepositoryID: previous.RepositoryID, UnitSHA256: previous.UnitSHA256, TimerSHA256: previous.TimerSHA256, RecoveryDeliveryID: previous.RecoveryDeliveryID})
	if err != nil {
		t.Fatal(err)
	}
	outcome, completed := abandonJSON(t, receipt), op.ID
	if inspection != nil {
		resolved, err := inspection.Complete(ctx, strings.Repeat("1", 64), outcome)
		if err != nil {
			t.Fatal(err)
		}
		outcome, completed = abandonJSON(t, resolved), resolved.OperationID
	}
	if err := activation.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTask(ctx, completed, ops.Succeeded, ops.TaskOutcome{Receipt: outcome}); err != nil {
		t.Fatal(err)
	}
	active, err := s.ReadNativeBackupPolicy(ctx, previous.BindingID)
	if err != nil || active.Revision != previous.Revision+1 || active.PolicySHA256 != strings.TrimPrefix(current.Hash(), "sha256:") {
		t.Fatal("reactivation did not bind the current operator policy", err)
	}
	scope, err := s.NativeCredentialScope(ctx, original.App, "")
	if err != nil || scope.PolicyHash != current.Hash() {
		t.Fatal("reactivation did not renew the data admission", scope.PolicyHash, err)
	}
	after, err := s.ReadEnginePermit(ctx, previous.BindingID)
	if err != nil || string(abandonJSON(t, before)) != string(abandonJSON(t, after)) {
		t.Fatal("reactivation changed the live runtime, allocation or fences", err)
	}
	material, err := readNativeCurrentRepositoryMaterial(ctx, s.db, after)
	if err != nil || material.PolicySHA256 != active.PolicySHA256 || material.CredentialRecord.PolicyHash == current.Hash() {
		t.Fatal("capture and recovery authorization still uses the credential's old policy", err)
	}
	assertNativeReactivatedLifecycle(t, s, desired)
	assertNativeReactivatedPreparation(t, s, active, requester)
	assertNativeReactivatedCredential(t, s, active, material)
}

func resumeNativeReactivation(t *testing.T, s *Store, op ops.Operation, activation *NativeBackupActivation) *NativeBackupInspection {
	t.Helper()
	ctx := t.Context()
	if err := activation.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTask(ctx, op.ID, ops.RecoveryRequired, ops.TaskOutcome{Error: result.InternalError}); err != nil {
		t.Fatal(err)
	}
	i, err := s.OpenNativeBackupInspection(ctx, op.ID, op.Requester)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = i.Close() })
	material, err := i.Material(ctx)
	if err != nil || material.PolicySHA256 != activation.plan.PolicySHA256 || material.CredentialRecord.PolicyHash == "sha256:"+material.PolicySHA256 {
		t.Fatal("interrupted reactivation used the credential's old policy", err)
	}
	review, err := i.Review(ctx, strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	resolve, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativeBackupResolve, App: op.App, SecretRef: review.ID, RecoveryOf: op.ID}, op.Requester, "resolve-reactivation")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, resolve.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimNativeBackupResolution(ctx, resolve.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claimed.Close() })
	return claimed
}

func nativeReactivationInput(t *testing.T, s *Store, desired policy.Desired) plan.Input {
	t.Helper()
	var binding data.ReplicaBindingID
	if err := s.db.QueryRowContext(t.Context(), "SELECT id FROM data_replica_bindings LIMIT 1").Scan(&binding); err != nil {
		t.Fatal(err)
	}
	permit, err := s.ReadEnginePermit(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	in := fixture(t)
	in.Desired = desired
	in.Snapshot.Versions.Caddy = target.Known("2.10.2")
	image := plan.Image{Digest: strings.Split(permit.Runtime.Image, "@")[1], Platform: target.Platform{OS: "linux", Arch: in.Snapshot.Arch}, ManifestDigest: target.Known(data.SpacetimeDBARM64Manifest)}
	in.Managed = &plan.ManagedFacts{Database: &permit.Reserved.Database, Archive: &permit.Reserved.Archive, Runtime: &permit.Runtime, EngineImage: image, Fences: []data.QuiescenceFence{}, NativeState: target.Known("running"), LoggingPrerequisite: target.Known(true), LoggingRootSHA256: strings.Repeat("a", 64), LoggingConfigSHA256: strings.Repeat("b", 64)}
	return in
}

func saveNativeReactivationDeployment(t *testing.T, s *Store, in plan.Input) plan.Plan {
	t.Helper()
	p, err := plan.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavePlan(t.Context(), p, in.Desired); err != nil {
		t.Fatal(err)
	}
	return p
}

func seedNativeReactivationPolicy(t *testing.T, s *Store, deployment plan.Plan, desired policy.Desired, requester string) NativeBackupPolicy {
	t.Helper()
	ctx := t.Context()
	scope, err := s.NativeCredentialScope(ctx, deployment.App, "")
	if err != nil {
		t.Fatal(err)
	}
	id, hash := strings.Repeat("a", 32), strings.Repeat("b", 64)
	credential := CredentialRecord{ID: id, Kind: "receipt", App: scope.App, Requester: requester, PlanID: "sha256:" + hash, PlanHash: "sha256:" + hash, TargetHash: "sha256:" + hash, PolicyHash: scope.PolicyHash, BindingID: scope.Database.BindingID, EpochID: scope.Archive.EpochID, Destination: scope.Archive.Destination.Reference, CredentialRef: scope.Archive.Destination.CredentialRef, Version: 1, ReceivedAt: time.Now().UTC()}
	provenance := backupcredentials.Plan{Requester: requester, Kind: backupcredentials.Kind, Version: credential.Version, Scope: backupcredentials.Scope{App: credential.App, CredentialRef: credential.CredentialRef, TargetHash: credential.TargetHash, PolicyHash: credential.PolicyHash, Binding: string(credential.BindingID), Epoch: string(credential.EpochID), Destination: string(credential.Destination)}}
	credential.PlanID = "sha256:" + nativePointHash(abandonJSON(t, provenance))
	credential.PlanHash = credential.PlanID
	if err := s.SaveNativeCredentialRecord(ctx, credential); err != nil {
		t.Fatal(err)
	}
	input, err := s.ReserveNativeRecoveryInput(ctx, deployment.Hash, requester, strings.Repeat("d", 32), hash)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := s.ClaimNativeRecoveryDelivery(ctx, input.ID, requester)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = delivery.Close() })
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativeRecoverySet, App: input.App, SecretRef: input.ID}, requester, input.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	recovery, err := s.RecordNativeRecoveryDelivery(ctx, delivery, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := delivery.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTask(ctx, op.ID, ops.Succeeded, ops.TaskOutcome{Receipt: abandonJSON(t, recovery)}); err != nil {
		t.Fatal(err)
	}
	var recoveryID string
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM data_native_recovery_deliveries WHERE operation_id=?", op.ID).Scan(&recoveryID); err != nil {
		t.Fatal(err)
	}
	op, _, err = s.CreateOperation(ctx, ops.Intent{Kind: ops.NativeBackupActivate, App: deployment.App, SecretRef: id}, requester, "previous-backup")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	previous := NativeBackupPolicy{Format: "brine-native-backup-policy-v1", BindingID: scope.Database.BindingID, EpochID: scope.Archive.EpochID, Revision: 1, OperationID: op.ID, RevisionPlanID: id, DeploymentPlanID: deployment.Hash, DesiredSHA256: strings.TrimPrefix(deployment.DesiredHash, "sha256:"), PolicySHA256: strings.TrimPrefix(scope.PolicyHash, "sha256:"), RepositoryID: hash, CredentialReference: credential.CredentialRef, CredentialVersion: 1, CredentialReceiptID: credential.ID, RecoveryReference: input.Reference, RecoveryVersion: 1, RecoveryDeliveryID: recoveryID, RecoverySHA256: hash, Backup: *desired.SpacetimePolicy, UnitSHA256: hash, TimerSHA256: hash, ActivatedAt: time.Now().UTC()}
	raw := abandonJSON(t, previous)
	abandonExec(t, s.db, "INSERT INTO data_native_backup_policies VALUES(?,?,?,?)", previous.BindingID, previous.Revision, previous.EpochID, raw)
	receipt := nativecapture.BackupPolicyReceipt{Kind: string(ops.NativeBackupActivate), OperationID: op.ID, BindingID: string(previous.BindingID), EpochID: string(previous.EpochID), PlanID: id, Revision: 1, RepositoryID: hash, PolicySHA256: nativePointHash(raw), Outcome: "active"}
	if err := s.CompleteTask(ctx, op.ID, ops.Succeeded, ops.TaskOutcome{Receipt: abandonJSON(t, receipt)}); err != nil {
		t.Fatal(err)
	}
	return previous
}

func assertNativeReactivatedPreparation(t *testing.T, s *Store, active NativeBackupPolicy, requester string) {
	t.Helper()
	ctx := t.Context()
	identity, err := datainit.ParseRequester(requester)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.SaveNativeTaskInput(ctx, NativeTaskInput{Requester: identity, Kind: ops.NativePrepare, BindingID: active.BindingID})
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepare, App: "hello", SecretRef: ref}, requester, "prepare-after-reactivation")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	fence, err := s.HoldNativePreparationFence(ctx, active.BindingID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := s.AcquireEnginePreparation(ctx, active.BindingID, fence.ID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guard.Close() }()
	prepared, err := s.BeginNativePreparation(ctx, guard, op.ID)
	if err != nil || prepared.intent.PolicySHA256 != active.PolicySHA256 {
		t.Fatal("new preparation retained the previous policy", err)
	}
	point := NativePreparedPointView{BindingID: active.BindingID, EpochID: active.EpochID, DatabaseID: prepared.intent.DatabaseID, IncarnationID: prepared.intent.IncarnationID, RuntimeVersion: prepared.intent.RuntimeVersion, RuntimeSHA256: prepared.intent.RuntimeSHA256, AllocationSHA256: prepared.intent.AllocationSHA256, PolicySHA256: prepared.intent.PolicySHA256}
	assertNativeReactivatedConsumers(t, s, point)
	permit, err := s.ReadEnginePermit(ctx, active.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := os.ReadFile("../spacetimedb/testdata/recovery-v1.wasm")
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := os.ReadFile("../spacetimedb/testdata/descriptor-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, point, proof := abandonFixtureCheckpoint(t, prepared.intent, permit.Runtime, artifact, descriptor)
	now := time.Now().UTC()
	point.PreparedAt, point.ExpiresAt, point.RetainUntil = now, now.Add(10*time.Minute), now.Add(31*24*time.Hour)
	proof.VerifiedAt, proof.ReaderDisposedAt = now, now
	stop := NativeStopObservation{Mode: "clean_exit", ContainerID: strings.Repeat("1", 64), FinishedAt: now, SourceAbsent: true, RestartJobsAbsent: true, IngressClosed: true}
	point.StopProofSHA256 = nativePointHash(abandonJSON(t, stop))
	point.RestoreReceiptSHA256 = nativePointHash(abandonJSON(t, proof))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	seedAbandonFixturePoint(t, tx, prepared.intent, prepared.raw, point, stop, proof, checkpoint, artifact, descriptor, op.App)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	receipt := nativecapture.TaskReceipt{Kind: string(ops.NativePrepare), OperationID: op.ID, BindingID: string(point.BindingID), EpochID: string(point.EpochID), PointID: point.PointID, PointSHA256: nativePointHash(abandonJSON(t, point)), Outcome: "verified", Engine: "spacetimedb", ModuleSHA256: point.ArtifactWasmSHA256, SchemaSHA256: point.SchemaSHA256, CaptureIntervalSeconds: 30, Loss: "unknown"}
	if err := s.CompleteTask(ctx, op.ID, ops.Succeeded, ops.TaskOutcome{Receipt: abandonJSON(t, receipt)}); err != nil {
		t.Fatal(err)
	}
	assertNativeReactivatedAbandonment(t, s, active, point, stop, requester)

	var retained CredentialRecord
	var raw []byte
	if err := s.db.QueryRowContext(ctx, "SELECT canonical FROM data_credential_records WHERE id=?", active.CredentialReceiptID).Scan(&raw); err != nil || json.Unmarshal(raw, &retained) != nil || retained.PolicyHash == "sha256:"+active.PolicySHA256 {
		t.Fatal("reactivation rewrote historical credential provenance", err)
	}
}

func assertNativeReactivatedConsumers(t *testing.T, s *Store, point NativePreparedPointView) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkNativePreparedCurrentPolicy(t.Context(), tx, point); err != nil {
		t.Fatal("fresh preparation cannot enter consumption or abandonment", err)
	}
	facts, _, err := readNativeLiveRestoreCurrentTx(t.Context(), tx, nativePreparedPoint{view: point})
	if err != nil || facts.PolicySHA256 != point.PolicySHA256 {
		t.Fatal("live restore reads the previous admission", err)
	}
	head := ManagedRouteState{App: facts.App, BindingID: point.BindingID, EpochID: point.EpochID, RuntimeSHA256: point.RuntimeSHA256}
	if err := nativeRouteWithdrawalCurrent(t.Context(), tx, head, "sha256:"+point.PolicySHA256, point); err != nil {
		t.Fatal("fresh preparation cannot enter lifecycle routing", err)
	}
	point.PolicySHA256 = strings.Repeat("0", 64)
	if err := checkNativePreparedCurrentPolicy(t.Context(), tx, point); err == nil {
		t.Fatal("old prepared point admitted after reactivation")
	}
}

func assertNativeReactivatedLifecycle(t *testing.T, s *Store, desired policy.Desired) {
	t.Helper()
	in := nativeReactivationInput(t, s, desired)
	var err error
	in.State, err = s.LoadBrineState(t.Context(), in.Snapshot.Identity, *in.Snapshot.Generation.Value)
	if err != nil {
		t.Fatal(err)
	}
	if in.State.Releases[0].Desired.PolicyHash == desired.PolicyHash {
		t.Fatal("backup activation rewrote the historical deployment policy")
	}
	in.Managed.OwnedNativeState = target.Known("running")
	in.Managed.BackupDesiredHash, err = s.ReadNativeBackupDesiredHash(t.Context(), in.Managed.Database.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []plan.ChangeKind{plan.StartApp, plan.StopApp, plan.RestartApp} {
		p, err := plan.BuildLifecycle(in, action)
		if err != nil || p.Kind == plan.Conflict {
			t.Errorf("lifecycle %s after successful reactivation: %v %v", action, p.Conflicts, err)
		}
	}
}

func assertNativeReactivatedCredential(t *testing.T, s *Store, active NativeBackupPolicy, material NativeRepositoryMaterial) {
	t.Helper()
	intent := nativeRepositoryIntent{record: nativeRepositoryIntentRecord{BindingID: active.BindingID, SourceEpochID: active.EpochID, PolicySHA256: active.PolicySHA256}, archive: data.ArchiveBinding{Destination: data.Destination{Reference: data.BackupDestinationRef(material.CredentialRecord.Destination), CredentialRef: material.CredentialReference}}}
	result := NativeInitializedRepositoryResult{Scope: material.Scope, VerifiedAt: time.Now().UTC()}
	if err := checkNativeInitializedCredential(t.Context(), s.db, intent, result); err != nil {
		t.Error("reactivated credential cannot initialize the next epoch", err)
	}
}

func nativeReactivationControlFixture(t *testing.T) (*Store, plan.Plan) {
	t.Helper()
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
	s, p, _ := managedControlStateFixtureAtRoot(t, false, false, "", module)
	return s, p
}

func assertNativeReactivatedAbandonment(t *testing.T, s *Store, active NativeBackupPolicy, point NativePreparedPointView, stop NativeStopObservation, requester string) {
	t.Helper()
	ctx := t.Context()
	identity, err := datainit.ParseRequester(requester)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.SaveNativeTaskInput(ctx, NativeTaskInput{Requester: identity, Kind: ops.NativePrepareAbandon, BindingID: point.BindingID, PointID: point.PointID})
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepareAbandon, App: "hello", SecretRef: ref}, requester, "abandon-after-reactivation")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	fixture := nativeAbandonFixture{state: s, operation: op, point: point, stop: stop}
	cap, observed := fixture.open(t)
	if _, err := s.CompleteNativeAbandonment(ctx, cap, observed); err != nil {
		t.Fatal("abandonment after reactivation", err)
	}
	if err := cap.Close(); err != nil {
		t.Fatal(err)
	}
	_, desired, err := s.LoadPlan(ctx, active.DeploymentPlanID)
	if err != nil {
		t.Fatal(err)
	}
	assertNativeReactivatedLifecycle(t, s, desired)
}

func TestNativeBackupReactivationAllowsCurrentDeployment(t *testing.T) {
	f := newNativeAbandonFixture(t)
	release, err := f.state.CurrentRelease(t.Context(), f.operation.App)
	if err != nil {
		t.Fatal(err)
	}
	_, desired, err := f.state.LoadPlan(t.Context(), release.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkNativeBackupOnlyPlan(t.Context(), f.state.db, release.App, desired); err != nil {
		t.Fatal("backup reactivation refused the already deployed desired state", err)
	}
}
