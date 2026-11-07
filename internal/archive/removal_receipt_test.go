package archive

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func removalReceiptFixture() RemovalReceipt {
	return RemovalReceipt{Format: "brine-sqlite-archive-receipt-v1", OperationID: "executing-resolver", AttemptOperationID: "original-attempt", PlanID: "sha256:" + strings.Repeat("a", 64), ArchiveID: data.ArchiveID(strings.Repeat("b", 32)), ManifestSHA256: strings.Repeat("c", 64), CompletionSHA256: strings.Repeat("d", 64), Outcome: "retired", RetainUntil: time.Date(2026, 12, 7, 10, 0, 0, 0, time.UTC)}
}

func TestRemovalReceiptRoundTripAndTaskIdentity(t *testing.T) {
	r := removalReceiptFixture()
	raw, err := EncodeRemovalReceipt(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRemovalReceipt(raw)
	if err != nil || got != r {
		t.Fatalf("receipt roundtrip = %#v, %v", got, err)
	}
	r.AttemptOperationID = r.OperationID
	if _, err := EncodeRemovalReceipt(r); err != nil {
		t.Fatal("direct attempt refused", err)
	}
	r.Outcome, r.ManifestSHA256, r.RetainUntil = "abandoned", "", time.Time{}
	if _, err := EncodeRemovalReceipt(r); err != nil {
		t.Fatal("pre-manifest abandon refused", err)
	}
	r.ManifestSHA256 = strings.Repeat("c", 64)
	if _, err := EncodeRemovalReceipt(r); err != nil {
		t.Fatal("prepared abandon refused", err)
	}
}

func requireReceiptRefusal(t *testing.T, raw []byte) {
	t.Helper()
	if _, err := DecodeRemovalReceipt(raw); err == nil {
		t.Fatal("invalid receipt admitted", string(raw))
	}
}

func TestRemovalReceiptStrictCodec(t *testing.T) {
	raw, _ := json.Marshal(removalReceiptFixture())
	text := string(raw)
	cases := map[string]string{
		"unknown":        strings.Replace(text, "{", "{\"extra\":1,", 1),
		"duplicate":      strings.Replace(text, "{", "{\"format\":\"brine-sqlite-archive-receipt-v1\",", 1),
		"alias":          strings.Replace(text, "operation_id", "Operation_ID", 1),
		"null":           strings.Replace(text, "\"executing-resolver\"", "null", 1),
		"missing":        strings.Replace(text, "\"attempt_operation_id\":\"original-attempt\",", "", 1),
		"trailing":       text + " {}",
		"native format":  strings.Replace(text, "sqlite", "native", 1),
		"empty manifest": strings.Replace(text, strings.Repeat("c", 64), "", 1),
		"zero retention": strings.Replace(text, "2026-12-07T10:00:00Z", "0001-01-01T00:00:00Z", 1),
		"bad operation":  strings.Replace(text, "executing-resolver", "../bad", 1),
		"bad attempt":    strings.Replace(text, "original-attempt", "../bad", 1),
		"bad outcome":    strings.Replace(text, "retired", "prepared", 1),
		"oversize":       text + strings.Repeat(" ", 4097),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) { requireReceiptRefusal(t, []byte(text)) })
	}
	padded := []byte(text + strings.Repeat(" ", 4096-len(raw)))
	if _, err := DecodeRemovalReceipt(padded); err != nil {
		t.Fatal("4096-byte valid receipt refused", err)
	}
	requireReceiptRefusal(t, append(padded, ' '))
	r := removalReceiptFixture()
	r.Outcome = "abandoned"
	invalid, _ := json.Marshal(r)
	requireReceiptRefusal(t, invalid)
}
