package ops

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
)

func TestNativeLiveRestoreIsReferenceOnlyTaskWithScopedStoppedReceipt(t *testing.T) {
	plan := "sha256:" + strings.Repeat("a", 64)
	for _, kind := range []Kind{NativeLiveRestore, NativeLiveRestoreResolve} {
		original := ""
		if kind == NativeLiveRestoreResolve {
			original = "original-operation"
		}
		intent := Intent{Kind: kind, App: "game", SecretRef: plan, RecoveryOf: original}
		if !ValidIntent(intent) || !kind.IsTask() || !CanTransitionFor(kind, Queued, Preflight) || CanTransitionFor(kind, Preflight, Starting) {
			t.Fatal("restore bypassed reference-only task lifecycle")
		}
		op := Operation{ID: "restore-operation", Kind: kind, App: "game", SecretRef: plan, RecoveryOf: original, State: Succeeded}
		if !ValidOperation(op) {
			t.Fatal("reference-only restore operation refused")
		}
		r := liverestore.Receipt{Format: "brine-native-live-restore-receipt-v1", OperationID: op.ID, PlanID: plan, OriginalOperationID: original, BindingID: data.ReplicaBindingID(strings.Repeat("1", 32)), SelectedPointID: strings.Repeat("2", 32), EpochID: data.ReplicaEpochID(strings.Repeat("3", 32)), CompletionProofSHA256: strings.Repeat("4", 64), Outcome: "restored_stopped"}
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodeTaskReceipt(op, raw); err != nil {
			t.Fatal(err)
		}
		for _, invalid := range []string{strings.Replace(string(raw), op.ID, "foreign-operation", 1), strings.Replace(string(raw), plan, "sha256:"+strings.Repeat("b", 64), 1), strings.Replace(string(raw), `"restored_stopped"`, `"running"`, 1), strings.Replace(string(raw), `"format":`, `"path":"/tmp/raw-data","format":`, 1)} {
			if _, err = DecodeTaskReceipt(op, []byte(invalid)); err == nil {
				t.Fatal("crossed/raw/live-start restore receipt admitted")
			}
		}
		intent.SecretRef = "caller-reference"
		if ValidIntent(intent) {
			t.Fatal("restore accepts arbitrary evidence reference")
		}
	}
}
