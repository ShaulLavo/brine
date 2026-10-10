package restore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRecoverabilityReceiptCannotBecomeExactPoint(t *testing.T) {
	engine, request, _ := setup(t, fixture(t, fixtureSQL))
	engine.CLI = &latestCLI{t: t, data: fixture(t, fixtureSQL)}
	request.Source = RestoreSource{Kind: LitestreamLTX, LTX: &LTXSource{BindingID: "b1", Epoch: "e1", TXID: 7, Recoverability: true}}
	receipt, err := engine.Test(context.Background(), request)
	if err != nil || receipt.PositionEvidence != "remote_dry_run_and_restore" || receipt.Barrier != nil || !receipt.Valid() {
		t.Fatal("recoverability receipt mislabeled", err)
	}
	exact := *receipt.Source.LTX
	exact.Recoverability = false
	source := RestoreSource{Kind: LitestreamLTX, LTX: &exact}
	if _, _, err := source.reference(); err == nil {
		t.Fatal("barrier-less receipt accepted as exact restore-point source")
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReceipt(raw); err != nil {
		t.Fatal("checked receipt did not round-trip", err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"operation_id":`, `"private_key":"PLANTED","operation_id":`, 1),
		strings.Replace(string(raw), `"marker":`, `"secret":"PLANTED","marker":`, 1),
		strings.Replace(string(raw), `"recoverability":true`, `"recoverability":true,"recoverability":false`, 1),
		strings.Replace(string(raw), `"remote_dry_run_and_restore"`, `"pinned_cli_exact_plan_and_successful_restore"`, 1),
	} {
		if _, err := DecodeReceipt([]byte(bad)); err == nil {
			t.Fatal("unchecked remote receipt accepted")
		}
	}
}
