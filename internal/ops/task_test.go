package ops

import (
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"strings"
	"testing"
)

func TestInitializationTaskReceiptIsClosedAndReferenceBound(t *testing.T) {
	op := Operation{Kind: DataInitApply, SecretRef: "sha256:" + strings.Repeat("a", 64), State: Succeeded}
	receipt := datainit.Operation{ID: strings.Repeat("1", 32), PlanID: op.SecretRef, State: "succeeded", Fence: data.FenceID(strings.Repeat("2", 32))}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeTaskReceipt(op, raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(string(raw), op.SecretRef, "sha256:"+strings.Repeat("b", 64), 1), strings.TrimSuffix(string(raw), "}") + `,"requester":"local-operator"}`, strings.Replace(string(raw), `"succeeded"`, `"mutation_intent"`, 1), strings.Replace(string(raw), receipt.ID, "arbitrary", 1), strings.Replace(string(raw), `"fence_id"`, `"fence"`, 1)} {
		if _, err = DecodeTaskReceipt(op, []byte(bad)); err == nil {
			t.Fatal("unsafe receipt accepted", bad)
		}
	}
}
