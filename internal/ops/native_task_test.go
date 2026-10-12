package ops

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/nativecapture"
)

func TestNativeTaskReceiptUsesExistingReferenceOnlyOperation(t *testing.T) {
	for _, kind := range []Kind{NativeCapture, NativeRestoreTest, NativePrepare} {
		intent := Intent{Kind: kind, App: "game", SecretRef: strings.Repeat("a", 64)}
		if !ValidIntent(intent) || !kind.IsTask() || !CanTransitionFor(kind, Queued, Preflight) || CanTransitionFor(kind, Preflight, Starting) {
			t.Fatal("native work bypasses task operation contract")
		}
		op := Operation{ID: "native-operation", Kind: kind, App: intent.App, SecretRef: intent.SecretRef, State: Succeeded}
		receipt := nativecapture.TaskReceipt{Kind: string(kind), OperationID: op.ID, BindingID: strings.Repeat("1", 32), EpochID: strings.Repeat("2", 32), PointID: strings.Repeat("3", 32), PointSHA256: strings.Repeat("4", 64), Outcome: "verified"}
		raw, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeTaskReceipt(op, raw); err != nil {
			t.Fatal(err)
		}
		for _, invalid := range nativeInvalidReceipt(raw, receipt) {
			if _, err := DecodeTaskReceipt(op, []byte(invalid)); err == nil {
				t.Fatal("crossed or unsafe native receipt admitted")
			}
		}
	}
}

func nativeInvalidReceipt(raw []byte, receipt nativecapture.TaskReceipt) []string {
	return []string{
		strings.Replace(string(raw), receipt.OperationID, "different-operation", 1),
		strings.Replace(string(raw), `"kind":`, `"requester":"forged","kind":`, 1),
		strings.Replace(string(raw), `"verified"`, `"unknown"`, 1),
		strings.Replace(string(raw), `"point_id":`, `"point_id":null,"point_id":`, 1),
		strings.Replace(string(raw), receipt.PointID, "caller-path", 1),
	}
}
