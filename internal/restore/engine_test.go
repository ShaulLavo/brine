package restore

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestEngineVerificationUsesTheSQLiteVerifier(t *testing.T) {
	engine, request, _ := setup(t, fixture(t, fixtureSQL))
	receipt, err := VerifySQLite(context.Background(), engine, request)
	if err != nil || !receipt.Valid() || receipt.Source.Kind != SQLiteSnapshot {
		t.Fatal("tagged verification lost SQLite checks or receipt", err)
	}
}

func TestSQLiteReceiptRefusesAnotherEngineTag(t *testing.T) {
	raw, err := os.ReadFile("testdata/sqlite-engine-receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	foreign := strings.Replace(string(raw), `"source":{`, `"source":{"engine":"spacetimedb",`, 1)
	if _, err = DecodeReceipt([]byte(foreign)); err == nil {
		t.Fatal("foreign engine tag consumed a checked SQLite receipt")
	}
}
