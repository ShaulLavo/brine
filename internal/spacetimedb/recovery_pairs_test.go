package spacetimedb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

func nativeRecoverySQL(t testing.TB) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/recovery-pairs-native-sql.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := decodeDocument(raw, DefaultSchemaLimits())
	if err != nil {
		t.Fatal(err)
	}
	return value.(map[string]any)
}

func recoverySQLResults(t testing.TB, value map[string]any) [][]any {
	t.Helper()
	var results [][]any
	for _, table := range []string{"left", "right", "marker"} {
		rows, err := decodeConfirmedSQLStatement(value[table], table == "marker")
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, rows)
	}
	return results
}

func TestConfirmedPairsUseActualNativeSQLShape(t *testing.T) {
	results := recoverySQLResults(t, nativeRecoverySQL(t))
	slices.Reverse(results[0])
	rows, err := decodeConfirmedRecoveryResults(results)
	if err != nil || len(rows) != 3 || rows[0].Sequence != "1" || !rows[2].Scheduled {
		t.Fatal("actual native pair/timestamp/schedule projection refused", err)
	}
	if rows[0].CommitTime != "1791772424950729" {
		t.Fatal("native singleton I64 timestamp changed meaning")
	}
}

func TestConfirmedPairsRefuseChangedNativeSQLSchemaAndWrites(t *testing.T) {
	cases := map[string]func(map[string]any){
		"reported write":          func(statement map[string]any) { statement["stats"].(map[string]any)["rows_updated"] = json.Number("1") },
		"unknown statement field": func(statement map[string]any) { statement["future"] = true },
		"unknown stats field":     func(statement map[string]any) { statement["stats"].(map[string]any)["future"] = json.Number("0") },
		"timestamp type": func(statement map[string]any) {
			statement["schema"].(map[string]any)["elements"].([]any)[2].(map[string]any)["algebraic_type"] = map[string]any{"String": []any{}}
		},
		"null rows": func(statement map[string]any) { statement["rows"] = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) { checkChangedSQLRefusal(t, mutate) })
	}
}

func checkChangedSQLRefusal(t *testing.T, mutate func(map[string]any)) {
	t.Helper()
	value := nativeRecoverySQL(t)["left"]
	mutate(value.([]any)[0].(map[string]any))
	if _, err := decodeConfirmedSQLStatement(value, false); !errors.Is(err, ErrRecoveryPairs) {
		t.Fatal("changed schema or SQL-write result admitted")
	}
}

func confirmedRecordFixture(t testing.TB) confirmedPairsRecord {
	t.Helper()
	rows, err := decodeConfirmedRecoveryResults(recoverySQLResults(t, nativeRecoverySQL(t)))
	if err != nil {
		t.Fatal(err)
	}
	return confirmedPairsRecord{
		Format: ConfirmedPairsFormat, Contract: RecoveryPairsContract, Basis: "confirmed_sql_lower_bound",
		Database: databaseHex, Publisher: publisherHex, Names: []string{"fixture"}, Program: currentHex,
		Wasm: "92f4efff417d8f17ba4329c46fd1bdc13c8e234137e6150ac33f62503cb212cc", Canonicalizer: CanonicalizerVersion,
		Schema: "f10d1efcbbdcec34434fa257946ca8a562ba1ad5f6b1b22b17fca14ca7eacc45", Marker: RecoverySchemaMarker, Rows: rows,
	}
}

func TestConfirmedPairsRetainedCodecOwnsCanonicalBytes(t *testing.T) {
	value := confirmedRecordFixture(t)
	record, err := newConfirmedPairsRecord(value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeConfirmedPairsRecord(record.Bytes())
	if err != nil || decoded.SHA256() != record.SHA256() || decoded.RowCount() != 3 {
		t.Fatal("retained record changed", err)
	}
	value.Names[0], value.Rows[0].Marker = "changed", "changed"
	raw, names := decoded.Bytes(), decoded.Names()
	raw[0], names[0] = 'x', "changed"
	if !bytes.Equal(decoded.Bytes(), record.Bytes()) || decoded.Names()[0] != "fixture" {
		t.Fatal("caller changed retained record")
	}
	if _, err := DecodeConfirmedPairsRecord(append(record.Bytes(), '\n')); !errors.Is(err, ErrRecoveryPairs) {
		t.Fatal("noncanonical journal bytes admitted")
	}
}

func TestConfirmedPairsRefuseFiniteContractViolations(t *testing.T) {
	cases := map[string]func(*confirmedPairsRecord){
		"basis finality":      func(v *confirmedPairsRecord) { v.Basis = "final" },
		"unsupported profile": func(v *confirmedPairsRecord) { v.Wasm = strings.Repeat("c", 64) },
		"schema drift":        func(v *confirmedPairsRecord) { v.Schema = strings.Repeat("c", 64) },
		"marker drift":        func(v *confirmedPairsRecord) { v.Marker = "other" },
		"gap":                 func(v *confirmedPairsRecord) { v.Rows = v.Rows[1:] },
		"duplicate":           func(v *confirmedPairsRecord) { v.Rows[1] = v.Rows[0] },
		"row marker":          func(v *confirmedPairsRecord) { v.Rows[0].Marker = "other" },
		"negative timestamp":  func(v *confirmedPairsRecord) { v.Rows[0].CommitTime = "-1" },
		"timestamp overflow":  func(v *confirmedPairsRecord) { v.Rows[0].CommitTime = "9223372036854775808" },
		"timestamp alias":     func(v *confirmedPairsRecord) { v.Rows[0].CommitTime = "01791772424950729" },
		"empty identity":      func(v *confirmedPairsRecord) { v.Database = "" },
		"names order":         func(v *confirmedPairsRecord) { v.Names = []string{"second", "first"} },
		"null rows":           func(v *confirmedPairsRecord) { v.Rows = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) { checkConfirmedRecordRefusal(t, mutate) })
	}
}

func checkConfirmedRecordRefusal(t *testing.T, mutate func(*confirmedPairsRecord)) {
	t.Helper()
	value := confirmedRecordFixture(t)
	mutate(&value)
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeConfirmedPairsRecord(raw); !errors.Is(err, ErrRecoveryPairs) {
		t.Fatal("invalid retained contract admitted")
	}
}

func TestConfirmedPairsRefuseUnknownAliasDuplicateAndNull(t *testing.T) {
	record, err := newConfirmedPairsRecord(confirmedRecordFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	raw := string(record.Bytes())
	for _, bad := range []string{
		"null", "{}", strings.Repeat(" ", MaxRecoveryPairsBytes+1),
		strings.Replace(raw, `"format":`, `"Format":`, 1),
		strings.Replace(raw, `"rows":`, `"future":true,"rows":`, 1),
		strings.Replace(raw, `"scheduled":false`, `"scheduled":null`, 1),
		strings.Replace(raw, `"scheduled":false`, `"scheduled":false,"scheduled":true`, 1),
	} {
		if _, err := DecodeConfirmedPairsRecord([]byte(bad)); !errors.Is(err, ErrRecoveryPairs) {
			t.Fatal("ambiguous record admitted")
		}
	}
}

func TestConfirmedPairsRejectUnsupportedArtifactBeforeNetwork(t *testing.T) {
	endpoint, credential, expected, fixture := observationFixture(t, "")
	wasm, err := os.ReadFile("../spec/testdata/managed-module.wasm")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := CanonicalSchemaV10(fixture.schema, DefaultSchemaLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, err = ObserveConfirmedPairs(context.Background(), endpoint, credential, expected, wasm, schema, DefaultObserverLimits())
	if !errors.Is(err, ErrRecoveryPairs) || len(fixture.paths) != 0 {
		t.Fatal("unsupported artifact reached lifecycle-running SQL or metadata")
	}
}
