package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
	"strings"
	"testing"
)

func TestNativeBackupRecoveryCLIUsesOriginalReferenceAndClosedResolverReceipt(t *testing.T) {
	for _, command := range []string{"inspect", "resolve"} {
		t.Run(command, func(t *testing.T) { checkNativeBackupRecoveryCLI(t, command) })
	}
}
func checkNativeBackupRecoveryCLI(t *testing.T, command string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	original := nativeCommandReceipt(ops.NativeCapture)
	originalRaw, _ := json.Marshal(original)
	reviewID := strings.Repeat("1", 64)
	receipt := nativecapture.BackupResolutionReceipt{Kind: string(ops.NativeBackupResolve), OperationID: "resolver-operation", ReviewID: reviewID, OriginalOperationID: original.OperationID, OriginalKind: original.Kind, BindingID: original.BindingID, EpochID: original.EpochID, OriginalSHA256: strings.Repeat("2", 64), ObservationSHA256: strings.Repeat("3", 64), CompletionSHA256: nativeCommandHash(originalRaw), Receipt: originalRaw, Outcome: "adopted"}
	calls := 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		calls++
		if command == "inspect" {
			if request.Op != "native_backup_inspect" || string(request.Args) != `{"operation_id":"`+original.OperationID+`"}` {
				t.Fatal("inspection supplied more than original reference")
			}
			return result.Success("brine host native_backup_inspect", dispatch.NativeBackupInspectionView{OperationID: original.OperationID, Kind: original.Kind, BindingID: original.BindingID, EpochID: original.EpochID, ObservationSHA256: receipt.ObservationSHA256, State: "verified_remote"}), nil
		}
		if request.Op == "operation" {
			raw, _ := json.Marshal(receipt)
			return result.Success("brine host operation", jobs.Status{Operation: ops.Operation{ID: receipt.OperationID, Kind: ops.NativeBackupResolve, App: "game", SecretRef: reviewID, RecoveryOf: original.OperationID, State: ops.Succeeded}, Events: []ops.Event{}, Outcome: &ops.TaskOutcome{Receipt: raw}}), nil
		}
		if request.Op != "native_backup_resolve" {
			t.Fatal("wrong recovery task")
		}
		var input dispatch.NativeBackupResolveArgs
		if json.Unmarshal(request.Args, &input) != nil || input.ReviewID != reviewID || input.IdempotencyKey != strings.Repeat("a", 32) {
			t.Fatal("resolver changed immutable review/key")
		}
		return result.Success("brine host native_backup_resolve", jobs.Accepted{Status: "accepted", OperationID: receipt.OperationID}), nil
	})
	reference := original.OperationID
	if command == "resolve" {
		reference = reviewID
	}
	args := []string{"backup", "recovery", command, reference, "--target", "fixture", "--json"}
	if command == "resolve" {
		args = append(args, "--idempotency-key", strings.Repeat("a", 32))
	}
	if err := Execute(deps, args); err != nil {
		t.Fatal(err)
	}
	expectedCalls := 1
	if command == "resolve" {
		expectedCalls = 2
	}
	if calls != expectedCalls || !strings.Contains(out.String(), original.OperationID) {
		t.Fatal("recovery lost original identity or repeated effects")
	}
}

func nativeCommandHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
