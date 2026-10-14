package nativecapture

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func abandonReceiptFixture() AbandonReceipt {
	id, hash := strings.Repeat("a", 32), strings.Repeat("b", 64)
	return AbandonReceipt{Kind: "native_prepare_abandon", OperationID: "abandon", InputSHA256: hash, PointID: id, PointSHA256: hash, PreparationOperationID: "prepare", FenceOwnerOperationID: "prepare", FenceID: id, BindingID: id, EpochID: id, SourceSealSHA256: hash, CurrentStopSHA256: hash, CurrentCodeSHA256: hash, CurrentRoutesSHA256: hash, ReleaseID: "release", StoppedDesiredRevision: 2, CompletedAt: time.Now().UTC(), Outcome: "abandoned_stopped"}
}

func TestAbandonReceiptHasOnlyClosedStoppedDisposition(t *testing.T) {
	receipt := abandonReceiptFixture()
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeAbandonReceipt(raw); err != nil {
		t.Fatal("bounded stopped disposition refused", err)
	}
	for _, attack := range []string{"running", "kind", "revision", "point", "hash", "time", "field"} {
		t.Run(attack, func(t *testing.T) { testAbandonReceiptAttack(t, receipt, attack) })
	}
}

func testAbandonReceiptAttack(t *testing.T, receipt AbandonReceipt, attack string) {
	t.Helper()
	switch attack {
	case "running":
		receipt.Outcome = "resumed"
	case "kind":
		receipt.Kind = "native_prepare"
	case "revision":
		receipt.StoppedDesiredRevision = 0
	case "point":
		receipt.PointID = "foreign"
	case "hash":
		receipt.CurrentCodeSHA256 = "unknown"
	case "time":
		receipt.CompletedAt = time.Time{}
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if attack == "field" {
		raw = append(raw[:len(raw)-1], []byte(`,"private_token":"secret"}`)...)
	}
	if _, err := DecodeAbandonReceipt(raw); err == nil {
		t.Fatal("foreign or live authority admitted")
	}
}
