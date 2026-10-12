//go:build linux

package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/policy"
)

func TestNativeMaterialRequiresSuccessfulTypedTaskOutcome(t *testing.T) {
	for _, outcome := range []string{"missing", "malformed", "typed"} {
		t.Run(outcome, func(t *testing.T) {
			checkNativeMaterialTaskOutcome(t, outcome)
		})
	}
}

func checkNativeMaterialTaskOutcome(t *testing.T, outcome string) {
	t.Helper()
	state, ctx := openTest(t), context.Background()
	op, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.NativeRecoverySet, App: "example", SecretRef: strings.Repeat("a", 32)}, "operator", outcome)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	if outcome != "typed" {
		if err := state.SetOperationState(ctx, op.ID, ops.Succeeded); err != nil {
			t.Fatal(err)
		}
		if outcome == "malformed" {
			_, err = state.db.ExecContext(ctx, "INSERT INTO operation_outcomes VALUES(?,?)", op.ID, []byte(`{"receipt":{"outcome":"stored"}}`))
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := readNativeMaterialOperation(ctx, state.db, op.ID, ops.NativeRecoverySet); err == nil {
			t.Fatal("terminal state admitted material without a successful typed receipt")
		}
		return
	}
	receipt := nativecapture.RecoveryReceipt{Kind: string(ops.NativeRecoverySet), OperationID: op.ID, BindingID: strings.Repeat("b", 32), EpochID: strings.Repeat("c", 32), Reference: "recovery", Version: 1, SHA256: strings.Repeat("d", 64), Outcome: "stored"}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.CompleteTask(ctx, op.ID, ops.Succeeded, ops.TaskOutcome{Receipt: raw}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readNativeMaterialOperation(ctx, state.db, op.ID, ops.NativeRecoverySet); err != nil {
		t.Fatal("successful typed task outcome refused", err)
	}
	if _, _, err := readNativeMaterialOperation(ctx, state.db, op.ID, ops.NativeBackupActivate); err == nil {
		t.Fatal("different material operation kind admitted")
	}
}

// This checks immutable policy selection only, not repository creation or S3 access.
func TestNativeHistoricalRepositorySelectsRenewedCredentialsWithoutChangingRecoveryKey(t *testing.T) {
	state, preparation, _, _ := nativePreparationFixture(t)
	binding, epoch := preparation.intent.BindingID, preparation.intent.EpochID
	hash := strings.Repeat("a", 64)
	id := strings.Repeat("b", 32)
	for _, revision := range []uint64{1, 2} {
		value := NativeBackupPolicy{Format: "brine-native-backup-policy-v1", BindingID: binding, EpochID: epoch, Revision: revision, OperationID: "backup-activation", RevisionPlanID: id, DeploymentPlanID: "sha256:" + hash, DesiredSHA256: hash, PolicySHA256: hash, RepositoryID: hash, CredentialReference: "backup", CredentialVersion: revision, CredentialReceiptID: id, RecoveryReference: "recovery", RecoveryVersion: 1, RecoveryDeliveryID: id, RecoverySHA256: hash, Backup: policy.SpacetimePolicy{LicenseUse: "fixture", LicenseAuthority: "fixture-v1", CaptureIntervalSeconds: 30, VerificationIntervalSeconds: 60, MaxRemoteLossSeconds: 60, MaxMaintenanceSeconds: 300}, UnitSHA256: hash, TimerSHA256: hash, ActivatedAt: time.Now().UTC()}
		raw, _ := json.Marshal(value)
		if _, err := state.db.ExecContext(context.Background(), "INSERT INTO data_native_backup_policies VALUES(?,?,?,?)", binding, revision, epoch, raw); err != nil {
			t.Fatal(err)
		}
	}
	selected, _, err := readNativeBackupPolicyScope(context.Background(), state.db, binding, epoch, hash, 0, 1)
	if err != nil || selected.CredentialVersion != 2 || selected.RecoveryVersion != 1 || selected.RepositoryID != hash {
		t.Fatal("renewed credential scope lost exact archive recovery", err)
	}
	original, _, err := readNativeBackupPolicyScope(context.Background(), state.db, binding, epoch, hash, 1, 1)
	if err != nil || original.CredentialVersion != 1 {
		t.Fatal("immutable original policy was rewritten", err)
	}
	if _, _, err := readNativeBackupPolicyScope(context.Background(), state.db, binding, epoch, hash, 0, 2); err == nil {
		t.Fatal("unrelated recovery key adopted a historical repository")
	}
}
