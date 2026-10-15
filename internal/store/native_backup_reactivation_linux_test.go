//go:build linux

package store

import (
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
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestNativeBackupReactivationOnInitializedEngine(t *testing.T) {
	s, original, _ := managedControlFixture(t)
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
	if err != nil || metadata.Material.CredentialRecord.PolicyHash == current.Hash() {
		t.Fatal("reactivation must reuse the original credential provenance", err)
	}
	if err := activation.BeginUnits(ctx); err != nil {
		t.Fatal(err)
	}
	receipt, err := activation.Commit(ctx, NativeBackupActivationObservation{RepositoryID: previous.RepositoryID, UnitSHA256: previous.UnitSHA256, TimerSHA256: previous.TimerSHA256, RecoveryDeliveryID: previous.RecoveryDeliveryID})
	if err != nil {
		t.Fatal(err)
	}
	if err := activation.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTask(ctx, op.ID, ops.Succeeded, ops.TaskOutcome{Receipt: abandonJSON(t, receipt)}); err != nil {
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
	assertNativeReactivatedPreparation(t, s, active, requester)
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
	op, _, err = s.CreateOperation(ctx, ops.Intent{Kind: ops.NativeBackupActivate, App: deployment.App, SecretRef: id}, requester, "previous-backup")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	previous := NativeBackupPolicy{Format: "brine-native-backup-policy-v1", BindingID: scope.Database.BindingID, EpochID: scope.Archive.EpochID, Revision: 1, OperationID: op.ID, RevisionPlanID: id, DeploymentPlanID: deployment.Hash, DesiredSHA256: strings.TrimPrefix(deployment.DesiredHash, "sha256:"), PolicySHA256: strings.TrimPrefix(scope.PolicyHash, "sha256:"), RepositoryID: hash, CredentialReference: credential.CredentialRef, CredentialVersion: 1, CredentialReceiptID: credential.ID, RecoveryReference: input.Reference, RecoveryVersion: 1, RecoveryDeliveryID: input.ID, RecoverySHA256: hash, Backup: *desired.SpacetimePolicy, UnitSHA256: hash, TimerSHA256: hash, ActivatedAt: time.Now().UTC()}
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
	var retained CredentialRecord
	var raw []byte
	if err := s.db.QueryRowContext(ctx, "SELECT canonical FROM data_credential_records WHERE id=?", active.CredentialReceiptID).Scan(&raw); err != nil || json.Unmarshal(raw, &retained) != nil || retained.PolicyHash == "sha256:"+active.PolicySHA256 {
		t.Fatal("reactivation rewrote historical credential provenance", err)
	}
}
