//go:build linux

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/result"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeBackupResolutionWorkspaceRejectsReplacementAndSymlinks(t *testing.T) {
	root := t.TempDir()
	heldPath := filepath.Join(root, "native-restores", strings.Repeat("1", 32))
	if err := os.MkdirAll(heldPath, 0700); err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenRoot(heldPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	relative, _ := filepath.Rel(root, heldPath)
	original, err := nativeUploadDirectoryIdentity(root, relative, held)
	if err != nil || original.Inode == 0 {
		t.Fatal("actual held directory", err)
	}
	if err := os.Rename(heldPath, heldPath+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(heldPath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeUploadDirectoryIdentity(root, relative, held); err == nil {
		t.Fatal("replacement adopted held identity")
	}
	if err := os.Remove(heldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(heldPath+"-retained", heldPath); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeUploadDirectoryIdentity(root, relative, held); err == nil {
		t.Fatal("symlink adopted owned workspace")
	}
}

func TestNativeBackupResolutionRequiresOriginalClaimAndPreservesTerminalOutcome(t *testing.T) {
	// The original first-activation fixture performs real Store reservations and
	// delivery completion, but never supplies a fabricated restic proof.
	state, plan, _ := managedControlStateFixture(t, false, true)
	ctx := context.Background()
	requester := "deploy:" + strings.Repeat("c", 64)
	scope, err := state.NativeCredentialScope(ctx, plan.App, "")
	if err != nil {
		t.Fatal(err)
	}
	record := CredentialRecord{ID: strings.Repeat("a", 32), Kind: "receipt", App: scope.App, Requester: requester, PlanID: "sha256:" + strings.Repeat("b", 64), PlanHash: "sha256:" + strings.Repeat("b", 64), TargetHash: "sha256:" + strings.Repeat("d", 64), PolicyHash: scope.PolicyHash, BindingID: scope.Database.BindingID, EpochID: scope.Archive.EpochID, Destination: scope.Archive.Destination.Reference, CredentialRef: scope.Archive.Destination.CredentialRef, Version: 1, ReceivedAt: time.Now().UTC()}
	if err := state.SaveNativeCredentialRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	input, err := state.ReserveNativeRecoveryInput(ctx, plan.Hash, requester, strings.Repeat("d", 32), strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := state.ClaimNativeRecoveryDelivery(ctx, input.ID, requester)
	if err != nil {
		t.Fatal(err)
	}
	recovery, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.NativeRecoverySet, App: input.App, SecretRef: input.ID}, requester, input.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = state.SetOperationState(ctx, recovery.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	recoveryReceipt, err := state.RecordNativeRecoveryDelivery(ctx, delivery, recovery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = delivery.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(recoveryReceipt)
	if err = state.CompleteTask(ctx, recovery.ID, ops.Succeeded, ops.TaskOutcome{Receipt: raw}); err != nil {
		t.Fatal(err)
	}
	planBackup, err := state.SaveNativeBackupRevisionPlan(ctx, plan.Hash, requester)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.NativeBackupActivate, App: planBackup.App, SecretRef: planBackup.ID}, requester, "original-backup")
	if err != nil {
		t.Fatal(err)
	}
	if err = state.SetOperationState(ctx, original.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	activation, err := state.ClaimNativeBackupActivation(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	material, err := activation.Metadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = activation.BeginInitialization(ctx); err != nil {
		t.Fatal(err)
	}
	if err = activation.AdmitRepository(ctx, material.Material.Scope, restic.CreateRepository, material.CreationGrantSHA256); err != nil {
		t.Fatal(err)
	}
	if err = activation.Close(); err != nil {
		t.Fatal(err)
	}
	if err = state.CompleteTask(ctx, original.ID, ops.RecoveryRequired, ops.TaskOutcome{Error: result.InternalError}); err != nil {
		t.Fatal(err)
	}
	before, err := state.ReadTaskOutcome(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRaw, _ := json.Marshal(before)
	if _, err = state.OpenNativeBackupInspection(ctx, original.ID, "operator"); err == nil {
		t.Fatal("foreign requester inspected original")
	}
	inspection, err := state.OpenNativeBackupInspection(ctx, original.ID, requester)
	if err != nil {
		t.Fatal(err)
	}
	current, err := inspection.Material(ctx)
	if err != nil {
		t.Fatal("actual original inspection material", err)
	}
	if err = inspection.AdmitRepository(ctx, current.Scope, restic.CreateRepository, material.CreationGrantSHA256); err == nil {
		t.Fatal("inspection repeated init")
	}
	if err = inspection.AdmitRepository(ctx, current.Scope, restic.WriteSnapshot, ""); err == nil {
		t.Fatal("inspection wrote snapshot")
	}
	if err = inspection.AdmitRepository(ctx, current.Scope, restic.ReadRepository, material.CreationGrantSHA256); err != nil {
		t.Fatal("actual issued grant fixed config inspection", err)
	}
	review, err := inspection.Review(ctx, strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err = inspection.Close(); err != nil {
		t.Fatal(err)
	}
	resolve, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.NativeBackupResolve, App: original.App, SecretRef: review.ID, RecoveryOf: original.ID}, requester, "backup-resolve")
	if err != nil {
		t.Fatal(err)
	}
	if err = state.SetOperationState(ctx, resolve.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	claim, err := state.ClaimNativeBackupResolution(ctx, resolve.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = claim.Close() }()
	actual, err := claim.Activation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = actual.RecordInitialized(ctx, restic.InitializedRepository{}); err == nil {
		t.Fatal("opaque initialization proof manufactured")
	}
	if _, err = claim.Complete(ctx, strings.Repeat("1", 64), []byte(`{}`)); err == nil {
		t.Fatal("resolver adopted empty receipt")
	}
	if err = claim.CheckReviewedObservation(ctx, strings.Repeat("2", 64)); err == nil {
		t.Fatal("changed inspection adopted")
	}
	after, err := state.ReadTaskOutcome(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterRaw, _ := json.Marshal(after)
	if !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatal("original outcome changed")
	}
	if _, err = readNativeBackupResolvedReceipt(ctx, state.db, ops.Operation{ID: original.ID, Requester: requester, App: original.App, Kind: ops.NativeBackupActivate, State: ops.RecoveryRequired}); err == nil {
		t.Fatal("nonterminal resolver admitted effective success")
	}
	permit, err := state.ReadEnginePermit(ctx, scope.Database.BindingID)
	fences := permit.Fences
	if err != nil {
		t.Fatal(err)
	}
	if len(fences) != 1 || fences[0].State != data.FenceHeld {
		t.Fatal("original fence released by inspection")
	}
}

func TestNativeBackupResolutionUploadExclusionPreservesExistingPermitAndOtherBindings(t *testing.T) {
	state, plan, _ := managedControlStateFixture(t, false, true)
	ctx := context.Background()
	requester, err := datainit.ParseRequester("deploy:" + strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	binding := plan.Managed.Database.BindingID
	reference, err := state.SaveNativeTaskInput(ctx, NativeTaskInput{Requester: requester, Kind: ops.NativeCapture, BindingID: binding})
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.NativeCapture, App: plan.App, SecretRef: reference}, requester.String(), "capture-exclusion")
	if err != nil {
		t.Fatal(err)
	}
	if err = state.SetOperationState(ctx, original.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	before, err := state.ReadEnginePermit(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	beforeRaw, _ := json.Marshal(before)
	if err = refusePendingNativeCaptureMutation(ctx, state.db, binding, "planned-mutation"); err != nil {
		t.Fatal("empty upload journal excluded mutation", err)
	}
	// This deliberately incomplete descriptor is refusal metadata, not evidence
	// of a captured/uploaded archive. It must never admit a point or resolver.
	descriptor := nativeUploadDescriptor{Format: "brine-native-upload-descriptor-v1", OperationID: original.ID, PointID: strings.Repeat("1", 32), Scope: restic.Scope{BindingID: string(binding), EpochID: string(before.Runtime.EpochID)}}
	raw, _ := json.Marshal(descriptor)
	if _, err = state.db.ExecContext(ctx, "INSERT INTO data_native_upload_descriptors VALUES(?,?,?,?,?)", original.ID, descriptor.PointID, raw, []byte{}, []byte{}); err != nil {
		t.Fatal(err)
	}
	if err = refusePendingNativeCaptureMutation(ctx, state.db, binding, "planned-mutation"); err == nil {
		t.Fatal("unfinished upload admitted planned mutation")
	}
	if err = refusePendingNativeCaptureMutation(ctx, state.db, binding, original.ID); err != nil {
		t.Fatal("actual original producer excluded from finishing own work", err)
	}
	other := data.ReplicaBindingID(strings.Repeat("9", 32))
	if err = refusePendingNativeCaptureMutation(ctx, state.db, other, "planned-mutation"); err != nil {
		t.Fatal("upload excluded unrelated binding", err)
	}
	after, err := state.ReadEnginePermit(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	afterRaw, _ := json.Marshal(after)
	if !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatal("upload exclusion changed runtime/fences or asserted quiescence")
	}
}
