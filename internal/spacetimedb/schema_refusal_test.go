package spacetimedb

import (
	"encoding/json"
	"errors"
	"testing"
)

func assertSchemaRefused(t *testing.T, schema map[string]any) {
	t.Helper()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalSchemaV10(raw, DefaultSchemaLimits()); !errors.Is(err, ErrSchema) {
		t.Fatal("malformed schema accepted", err)
	}
}

func TestConflictingExplicitNameMappingsRefuse(t *testing.T) {
	schema := schemaObject(t)
	names := sectionObject(schema, "ExplicitNames").(map[string]any)
	entries := names["entries"].([]any)
	source := entries[0].(map[string]any)["Table"].(map[string]any)["source_name"]
	names["entries"] = append(entries, map[string]any{"Table": map[string]any{
		"source_name": source, "canonical_name": "conflicting_alias",
	}})
	assertSchemaRefused(t, schema)
}

func TestDuplicateNamedElementsRefuse(t *testing.T) {
	schema := schemaObject(t)
	types := sectionObject(schema, "Typespace").(map[string]any)["types"].([]any)
	fields := types[1].(map[string]any)["Product"].(map[string]any)["elements"].([]any)
	fields[1].(map[string]any)["name"] = fields[0].(map[string]any)["name"]
	assertSchemaRefused(t, schema)

	schema = schemaObject(t)
	reverseParameters(schema)
	params := sectionObject(schema, "Reducers").([]any)[0].(map[string]any)["params"].(map[string]any)["elements"].([]any)
	params[1].(map[string]any)["name"] = params[0].(map[string]any)["name"]
	assertSchemaRefused(t, schema)
}

func TestAnonymousParametersMayRepeat(t *testing.T) {
	schema := schemaObject(t)
	reverseParameters(schema)
	params := sectionObject(schema, "Reducers").([]any)[0].(map[string]any)["params"].(map[string]any)["elements"].([]any)
	for _, param := range params {
		param.(map[string]any)["name"] = map[string]any{"none": []any{}}
	}
	schemaHash(t, schema)
}

func TestImpossibleTableColumnReferencesRefuse(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"primary_range", func(table map[string]any) { table["primary_key"] = columnNumbers("2147483647") }},
		{"primary_multiple", func(table map[string]any) { table["primary_key"] = columnNumbers("0", "1") }},
		{"index_range", func(table map[string]any) { setIndexColumns(table, "2147483647") }},
		{"index_duplicate", func(table map[string]any) { setIndexColumns(table, "0", "0") }},
		{"index_empty", func(table map[string]any) { setIndexColumns(table) }},
		{"constraint_range", func(table map[string]any) { setConstraintColumns(table, "2147483647") }},
		{"constraint_duplicate", func(table map[string]any) { setConstraintColumns(table, "0", "0") }},
		{"constraint_empty", func(table map[string]any) { setConstraintColumns(table) }},
		{"sequence_range", setInvalidSequenceColumn},
		{"default_range", func(table map[string]any) {
			table["default_values"] = []any{map[string]any{"col_id": json.Number("2147483647"), "value": "01"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema := schemaObject(t)
			test.mutate(sectionObject(schema, "Tables").([]any)[0].(map[string]any))
			assertSchemaRefused(t, schema)
		})
	}
}

func columnNumbers(values ...string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = json.Number(value)
	}
	return result
}

func setIndexColumns(table map[string]any, values ...string) {
	index := table["indexes"].([]any)[0].(map[string]any)
	index["algorithm"] = map[string]any{"BTree": columnNumbers(values...)}
}

func setConstraintColumns(table map[string]any, values ...string) {
	constraint := table["constraints"].([]any)[0].(map[string]any)
	constraint["data"] = map[string]any{"Unique": map[string]any{"columns": columnNumbers(values...)}}
}

func setInvalidSequenceColumn(table map[string]any) {
	table["sequences"] = []any{map[string]any{
		"source_name": map[string]any{"some": "sequence"}, "column": json.Number("2147483647"),
		"start": map[string]any{"none": []any{}}, "min_value": map[string]any{"none": []any{}},
		"max_value": map[string]any{"none": []any{}}, "increment": json.Number("1"),
	}}
}

func TestAnonymousTableColumnRefuses(t *testing.T) {
	schema := schemaObject(t)
	types := sectionObject(schema, "Typespace").(map[string]any)["types"].([]any)
	fields := types[1].(map[string]any)["Product"].(map[string]any)["elements"].([]any)
	fields[0].(map[string]any)["name"] = map[string]any{"none": []any{}}
	assertSchemaRefused(t, schema)
}
