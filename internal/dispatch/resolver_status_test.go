package dispatch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

func resolverStatusFixture() jobs.Status {
	original := nativecapture.BackupPolicyReceipt{Kind: string(ops.NativeBackupActivate), OperationID: "original-activation", BindingID: strings.Repeat("a", 32), EpochID: strings.Repeat("b", 32), PlanID: strings.Repeat("c", 32), Revision: 1, PolicySHA256: strings.Repeat("d", 64), RepositoryID: strings.Repeat("e", 64), Outcome: "active"}
	originalRaw, _ := json.Marshal(original)
	digest := sha256.Sum256(originalRaw)
	receipt := nativecapture.BackupResolutionReceipt{Kind: string(ops.NativeBackupResolve), OperationID: "activation-resolver", ReviewID: strings.Repeat("f", 64), OriginalOperationID: original.OperationID, OriginalKind: original.Kind, BindingID: original.BindingID, EpochID: original.EpochID, OriginalSHA256: strings.Repeat("1", 64), ObservationSHA256: strings.Repeat("2", 64), CompletionSHA256: hex.EncodeToString(digest[:]), Receipt: originalRaw, Outcome: "adopted"}
	raw, _ := json.Marshal(receipt)
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	return jobs.Status{Operation: ops.Operation{ID: receipt.OperationID, Kind: ops.NativeBackupResolve, App: "game", SecretRef: receipt.ReviewID, RecoveryOf: original.OperationID, State: ops.Succeeded, CreatedAt: now, UpdatedAt: now}, Events: []ops.Event{}, Outcome: &ops.TaskOutcome{Receipt: raw}}
}
func TestNativeBackupActivationResolverStatusWireRoundtrip(t *testing.T) {
	status := resolverStatusFixture()
	if !status.Outcome.Valid(status.Operation) {
		t.Fatal("current producer receipt fixture invalid")
	}
	encoded, _ := json.Marshal(result.Success("brine host operation", status))
	decoded, err := DecodeResponse(encoded, "operation")
	if err != nil {
		t.Fatal("valid succeeded resolver status rejected", err)
	}
	actual, ok := decoded.Data.(jobs.Status)
	if !ok || actual.Operation.RecoveryOf != status.Operation.RecoveryOf || actual.Outcome == nil {
		t.Fatal("resolver original reference/outcome lost")
	}
	for _, wire := range []string{
		strings.Replace(string(encoded), `"recovery_of":"original-activation",`, "", 1),
		strings.Replace(string(encoded), `"recovery_of":"original-activation"`, `"recovery_of":null`, 1),
		strings.Replace(string(encoded), `"recovery_of":"original-activation"`, `"recovery_of":"foreign-activation"`, 1),
		strings.Replace(string(encoded), `"original_operation_id":"original-activation"`, `"original_operation_id":"foreign-activation"`, 1),
		strings.Replace(string(encoded), `"completion_sha256":`, `"private_reason":"unrelated","completion_sha256":`, 1),
	} {
		if _, err := DecodeResponse([]byte(wire), "operation"); err == nil {
			t.Fatal("malformed or rebound resolver status accepted")
		}
	}
}
func TestEveryCurrentResolverStatusKeepsRequiredOriginalReference(t *testing.T) {
	for _, kind := range []ops.Kind{ops.Resolve, ops.ModulePublishResolve, ops.NativeLiveRestoreResolve, ops.NativeRetireResolve, ops.NativeBackupResolve, ops.ArchiveMaintenanceResolve} {
		t.Run(string(kind), func(t *testing.T) { testResolverStatusKind(t, kind) })
	}
}
func testResolverStatusKind(t *testing.T, kind ops.Kind) {
	t.Helper()
	status := resolverStatusFixture()
	status.Operation.Kind, status.Operation.State, status.Outcome = kind, ops.Preflight, nil
	status.Operation.SecretRef = "sha256:" + strings.Repeat("a", 64)
	if kind == ops.NativeBackupResolve {
		status.Operation.SecretRef = strings.Repeat("a", 64)
	}
	if kind == ops.Resolve {
		status.Operation.PlanID, status.Operation.SecretRef = "sha256:"+strings.Repeat("a", 64), ""
	}
	if !ops.ValidOperation(status.Operation) {
		t.Fatal("current resolver declaration invalid", kind)
	}
	encoded, _ := json.Marshal(result.Success("brine host operation", status))
	if _, err := DecodeResponse(encoded, "operation"); err != nil {
		t.Fatal("current resolver status refused", kind, err)
	}
	for _, wire := range []string{strings.Replace(string(encoded), `"recovery_of":"original-activation",`, "", 1), strings.Replace(string(encoded), `"recovery_of":"original-activation"`, `"recovery_of":null`, 1)} {
		if _, err := DecodeResponse([]byte(wire), "operation"); err == nil {
			t.Fatal("resolver lost required original reference", kind)
		}
	}
}
