package spacetimedb

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestPublishedFixtureProfilesKeepConfirmedAndPassiveRecovery(t *testing.T) {
	raw, err := os.ReadFile("../../fixtures/spacetimedb-app/module/artifact-pins.json")
	if err != nil {
		t.Fatal(err)
	}
	var pins struct {
		Modules map[string]string `json:"modules"`
	}
	if err := json.Unmarshal(raw, &pins); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"v1", "logic-only", "additive", "default-column"} {
		t.Run(variant, func(t *testing.T) { checkPublishedFixtureRecovery(t, variant, pins.Modules[variant]) })
	}
}

func checkPublishedFixtureRecovery(t *testing.T, variant, wasm string) {
	t.Helper()
	if len(wasm) != 64 {
		t.Fatal("published fixture pin missing")
	}
	shape := variant
	if variant == "logic-only" {
		shape = "v1"
	}
	value, schema := passiveRecoveryFixture(t, shape)
	value["current_program_sha256"] = wasm
	pairs, err := RecoveryPairsFromPassive(decodePassiveRecoveryFixture(t, value), schema)
	if err != nil || pairs.RowCount() != 3 || string(pairs.ArtifactSHA256()) != wasm {
		t.Fatal("published fixture cannot prove recovered pairs", err)
	}
	confirmed := confirmedRecordFixture(t)
	confirmed.Wasm, confirmed.Schema = wasm, schema.SHA256()
	confirmed.Program = pairs.value.Program
	record, err := newConfirmedPairsRecord(confirmed)
	if err != nil || CheckConfirmedLowerBound(record, pairs) != nil {
		t.Fatal("published fixture cannot prove confirmed lower bound", err)
	}
	if SupportsConfirmedRecoveryPairs(wasm, strings.Repeat("e", 64)) {
		t.Fatal("published artifact admitted an unrelated schema")
	}
	value["current_program_sha256"] = strings.Repeat("e", 64)
	if _, err := RecoveryPairsFromPassive(decodePassiveRecoveryFixture(t, value), schema); !errors.Is(err, ErrRecoveryPairs) {
		t.Fatal("matching schema admitted an unknown artifact", err)
	}
}
