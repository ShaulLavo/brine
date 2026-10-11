package restore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func spacetimeSourceFixture() SpacetimeDBSource {
	return SpacetimeDBSource{BindingID: data.ReplicaBindingID(strings.Repeat("a", 32)), EpochID: data.ReplicaEpochID(strings.Repeat("b", 32)), CaptureID: strings.Repeat("c", 32), RepositorySnapshot: strings.Repeat("d", 64), ManifestSHA256: strings.Repeat("e", 64)}
}

func TestRestoreSourceTagsRefuseForeignOrIncompleteEvidence(t *testing.T) {
	source, err := SpacetimeDBSourceVariant(spacetimeSourceFixture())
	if err != nil {
		t.Fatal("SpacetimeDB source required a SQLite/LTX point", err)
	}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Source
	if json.Unmarshal(raw, &decoded) != nil || decoded.Engine() != data.EngineSpacetimeDB {
		t.Fatal("SpacetimeDB source lost its tag")
	}
	if _, ok := decoded.SQLite(); ok {
		t.Fatal("SpacetimeDB source became a SQLite source")
	}
	for _, forged := range []string{
		`null`, `{}`, `{"engine":"unknown","sqlite":{}}`,
		strings.Replace(string(raw), `"engine":"spacetimedb"`, `"engine":"sqlite"`, 1),
		strings.Replace(string(raw), `"engine":"spacetimedb"`, `"engine":"spacetimedb","engine":"sqlite"`, 1),
		strings.Replace(string(raw), `"manifest_sha256":`, `"txid":7,"manifest_sha256":`, 1),
		strings.Replace(string(raw), `"manifest_sha256":`, `"manifest_sha256":null,"ignored":`, 1),
		string(raw) + `{}`,
	} {
		if json.Unmarshal([]byte(forged), &decoded) == nil {
			t.Fatal("unchecked engine source accepted")
		}
	}
	var sqlite SQLiteSource
	if json.Unmarshal(raw, &sqlite) == nil {
		t.Fatal("SpacetimeDB evidence decoded as a concrete SQLite source")
	}
}

func TestEngineVerificationRefusesBeforeReadersOrWorkspace(t *testing.T) {
	root := t.TempDir() + "/must-remain-absent"
	reads := 0
	engine := &SQLiteEngine{Root: root, Bindings: BindingReaderFunc(func(context.Context, string, string) (Binding, error) {
		reads++
		return Binding{}, errors.New("must not read")
	}), Credentials: CredentialReaderFunc(func(context.Context, string) (Credentials, error) {
		reads++
		return Credentials{}, errors.New("must not read")
	})}
	verifier := Verifier{SQLite: engine}
	spacetime, err := SpacetimeDBVerification("fixture", spacetimeSourceFixture(), data.SpacetimeDBSchemaObservation{State: data.VerifiedEmpty, DatabaseID: data.DatabaseID(strings.Repeat("f", 32)), ObservedAt: time.Now().UTC()}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = verifier.Test(context.Background(), spacetime); !errors.Is(err, data.ErrUnsupportedEngine) {
		t.Fatal("unfinished engine verification admitted", err)
	}
	for _, request := range []Verification{
		{},
		{source: spacetime.source, schemaEngine: data.EngineSQLite, budget: time.Second},
		{source: Source{kind: "unknown"}, schemaEngine: "unknown", budget: time.Second},
	} {
		if _, err = verifier.Test(context.Background(), request); err == nil {
			t.Fatal("cross-engine or incomplete verification admitted")
		}
	}
	if reads != 0 {
		t.Fatal("engine refusal occurred after binding or credential access")
	}
	if _, err = os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("engine refusal created a restore workspace", err)
	}
}

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
