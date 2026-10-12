//go:build linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
)

func TestNativeFreshFencedDeliveryReachesFirstBackupInitialization(t *testing.T) {
	for _, splice := range []string{"exact", "binding", "epoch", "reference", "version", "sha"} {
		t.Run(splice, func(t *testing.T) { checkNativeFreshBackupInitialization(t, splice) })
	}
}

func TestNativeCredentialRenewalPreservesAnInitializedRecoveryFence(t *testing.T) {
	state, plan, _ := managedControlFixture(t)
	ctx := context.Background()
	fence, err := state.HoldDataFence(ctx, plan.Managed.Database.DatabaseID, "backup-recovery")
	if err != nil {
		t.Fatal(err)
	}
	before, err := state.ReadEnginePermit(ctx, plan.Managed.Database.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := state.NativeCredentialScope(ctx, plan.App, "")
	if err != nil || !scope.FenceHeld {
		t.Fatal(err)
	}
	hash := "sha256:" + strings.Repeat("b", 64)
	record := CredentialRecord{ID: strings.Repeat("a", 32), Kind: "receipt", App: scope.App, Requester: "deploy:" + strings.Repeat("c", 64), PlanID: hash, PlanHash: hash, TargetHash: hash, PolicyHash: scope.PolicyHash, BindingID: scope.Database.BindingID, EpochID: scope.Archive.EpochID, Destination: scope.Archive.Destination.Reference, CredentialRef: scope.Archive.Destination.CredentialRef, Version: 2, ReceivedAt: time.Now().UTC()}
	if err := state.SaveNativeCredentialRecord(ctx, record); err != nil {
		t.Fatal("material-only renewal could not resume fenced backup inspection", err)
	}
	after, err := state.ReadEnginePermit(ctx, scope.Database.BindingID)
	beforeRaw, _ := json.Marshal(before)
	afterRaw, _ := json.Marshal(after)
	if err != nil || string(beforeRaw) != string(afterRaw) {
		t.Fatal("credential renewal changed runtime or original recovery fence", err)
	}
	held := 0
	for _, retained := range after.Fences {
		if retained.State != data.FenceHeld {
			continue
		}
		if retained.ID != fence.ID || retained.OperationID != "backup-recovery" {
			t.Fatal("credential renewal replaced recovery fence owner")
		}
		held++
	}
	if held != 1 {
		t.Fatal("credential renewal released recovery fence")
	}
}

func checkNativeFreshBackupInitialization(t *testing.T, splice string) {
	t.Helper()
	state, plan, _ := managedControlStateFixture(t, false, true)
	ctx := context.Background()
	requester := "deploy:" + strings.Repeat("c", 64)
	scope, err := state.NativeCredentialScope(ctx, plan.App, "")
	if err != nil || !scope.FenceHeld {
		t.Fatal("fresh unpublished credential scope", err)
	}
	record := CredentialRecord{ID: strings.Repeat("a", 32), Kind: "receipt", App: scope.App, Requester: requester, PlanID: "sha256:" + strings.Repeat("b", 64), PlanHash: "sha256:" + strings.Repeat("b", 64), TargetHash: "sha256:" + strings.Repeat("d", 64), PolicyHash: scope.PolicyHash, BindingID: scope.Database.BindingID, EpochID: scope.Archive.EpochID, Destination: scope.Archive.Destination.Reference, CredentialRef: scope.Archive.Destination.CredentialRef, Version: 1, ReceivedAt: time.Now().UTC()}
	if err := state.SaveNativeCredentialRecord(ctx, record); err != nil {
		t.Fatal("first credential delivery behind original fence", err)
	}
	input, err := state.ReserveNativeRecoveryInput(ctx, plan.Hash, requester, strings.Repeat("d", 32), strings.Repeat("e", 64))
	if err != nil {
		t.Fatal("first independent recovery delivery", err)
	}
	delivery, err := state.ClaimNativeRecoveryDelivery(ctx, input.ID, requester)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.NativeRecoverySet, App: input.App, SecretRef: input.ID}, requester, input.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	receipt, err := state.RecordNativeRecoveryDelivery(ctx, delivery, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := delivery.Close(); err != nil {
		t.Fatal(err)
	}
	switch splice {
	case "binding":
		receipt.BindingID = strings.Repeat("9", 32)
	case "epoch":
		receipt.EpochID = strings.Repeat("9", 32)
	case "reference":
		receipt.Reference = "different-recovery"
	case "version":
		receipt.Version++
	case "sha":
		receipt.SHA256 = strings.Repeat("9", 64)
	}
	raw, _ := json.Marshal(receipt)
	if err := state.CompleteTask(ctx, op.ID, ops.Succeeded, ops.TaskOutcome{Receipt: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ReadNativeBackupPolicy(ctx, scope.Database.BindingID); !errors.Is(err, ErrNotFound) {
		t.Fatal("test must have no already-active policy", err)
	}
	backup, err := state.SaveNativeBackupRevisionPlan(ctx, plan.Hash, requester)
	if err != nil {
		t.Fatal("first backup plan", err)
	}
	op, _, err = state.CreateOperation(ctx, ops.Intent{Kind: ops.NativeBackupActivate, App: backup.App, SecretRef: backup.ID}, requester, "first-backup")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	activation, err := state.ClaimNativeBackupActivation(ctx, op.ID)
	if splice != "exact" {
		if err == nil {
			_ = activation.Close()
			t.Fatal("unrelated successful recovery receipt admitted")
		}
		return
	}
	if err != nil {
		t.Fatal("first backup activation claim", err)
	}
	defer func() { _ = activation.Close() }()
	metadata, err := activation.Metadata(ctx)
	if err != nil || metadata.Previous.Revision != 0 || metadata.Material.Scope.RepositoryID != "" {
		t.Fatal("first owned repository scope", err)
	}
	if err := activation.BeginInitialization(ctx); err != nil {
		t.Fatal(err)
	}
	if err := activation.AdmitRepository(ctx, metadata.Material.Scope, restic.CreateRepository, metadata.CreationGrantSHA256); err != nil {
		t.Fatal("first initialization admission", err)
	}
	if err := activation.AdmitRepository(ctx, metadata.Material.Scope, restic.CreateRepository, metadata.CreationGrantSHA256); err == nil {
		t.Fatal("unknown initialization replay admitted")
	}
	fresh, err := state.NativeCredentialScope(ctx, plan.App, "")
	if err != nil || !fresh.FenceHeld {
		t.Fatal("material delivery or activation released original fence", err)
	}
}
