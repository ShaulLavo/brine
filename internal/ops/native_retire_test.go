package ops

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
)

func TestNativeRetireTaskRetainsOriginalOperationAndClosedArchiveReceipt(t *testing.T) {
	plan := "sha256:" + strings.Repeat("a", 64)
	for _, kind := range []Kind{NativeRetire, NativeRetireResolve} {
		original := ""
		if kind == NativeRetireResolve {
			original = "original-retire"
		}
		intent := Intent{Kind: kind, App: "game", SecretRef: plan, RecoveryOf: original}
		if !ValidIntent(intent) || !kind.IsTask() || CanTransitionFor(kind, Preflight, Starting) {
			t.Fatal("retirement bypassed task lifecycle")
		}
		op := Operation{ID: "retire-operation", Kind: kind, App: "game", SecretRef: plan, RecoveryOf: original, State: Succeeded}
		if !ValidOperation(op) {
			t.Fatal("scoped retirement operation refused")
		}
		receipt := archive.TaskReceipt{Format: "brine-native-archive-receipt-v1", OperationID: op.ID, PlanID: plan, OriginalOperationID: original, ArchiveID: data.ArchiveID(strings.Repeat("1", 32)), ManifestSHA256: strings.Repeat("2", 64), CompletionSHA256: strings.Repeat("3", 64), Outcome: "retired"}
		raw, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodeTaskReceipt(op, raw); err != nil {
			t.Fatal(err)
		}
		for _, invalid := range []string{strings.Replace(string(raw), op.ID, "foreign-retire", 1), strings.Replace(string(raw), plan, "sha256:"+strings.Repeat("b", 64), 1), strings.Replace(string(raw), `"retired"`, `"purged"`, 1), strings.Replace(string(raw), `"format":`, `"force":true,"format":`, 1)} {
			if _, err = DecodeTaskReceipt(op, []byte(invalid)); err == nil {
				t.Fatal("crossed or purge-capable retirement receipt admitted")
			}
		}
	}
}
