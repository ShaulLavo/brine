package spacetimedb

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeDefaultColumnPayload(t *testing.T) {
	canonical, err := CanonicalSchemaV10(actualSchema(t, "schema-default-column.json"), DefaultSchemaLimits())
	if err != nil {
		t.Fatal("pinned native default-column schema refused", err)
	}
	if !bytes.Contains(canonical.Bytes(), []byte(`"value":"07"`)) {
		t.Fatal("native default bytes changed")
	}
}

func TestMalformedDefaultByteStringsRefuse(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
	}{
		{"integer_array", []any{json.Number("7")}},
		{"number", json.Number("7")}, {"null", nil}, {"boolean", true},
		{"object", map[string]any{"bytes": "07"}},
		{"odd", "7"}, {"odd_longer", "007"}, {"invalid", "zz"},
		{"prefix", "0x07"}, {"whitespace", "07 08"}, {"uppercase", "AB"},
		{"unicode", "ＡＢ"}, {"oversized", strings.Repeat("01", DefaultSchemaLimits().MaxBytes)},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema := schemaObject(t)
			table := sectionObject(schema, "Tables").([]any)[0].(map[string]any)
			table["default_values"] = []any{map[string]any{"col_id": json.Number("0"), "value": test.value}}
			assertSchemaRefused(t, schema)
		})
	}
}

func TestDefaultByteOrderRemainsMeaningful(t *testing.T) {
	schema := schemaObject(t)
	table := sectionObject(schema, "Tables").([]any)[0].(map[string]any)
	value := map[string]any{"col_id": json.Number("1"), "value": "0102030405060708"}
	table["default_values"] = []any{value}
	before := schemaHash(t, schema)
	value["value"] = "0807060504030201"
	if schemaHash(t, schema) == before {
		t.Fatal("default byte order was discarded")
	}
}

func TestEmptyDefaultByteStringForUnitType(t *testing.T) {
	schema := schemaObject(t)
	table := sectionObject(schema, "Tables").([]any)[0].(map[string]any)
	types := sectionObject(schema, "Typespace").(map[string]any)["types"].([]any)
	index, err := integer(table["product_type_ref"])
	if err != nil {
		t.Fatal(err)
	}
	fields := types[index].(map[string]any)["Product"].(map[string]any)["elements"].([]any)
	fields[1].(map[string]any)["algebraic_type"] = map[string]any{"Product": map[string]any{"elements": []any{}}}
	table["default_values"] = []any{map[string]any{"col_id": json.Number("1"), "value": ""}}
	schemaHash(t, schema)
}
