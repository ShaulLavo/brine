package spacetimedb

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"testing"
)

func actualSchema(t *testing.T, name string) []byte {
	t.Helper()
	root, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	raw, err := root.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func schemaObject(t *testing.T) map[string]any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(actualSchema(t, "schema-before.json")))
	d.UseNumber()
	var value map[string]any
	if err := d.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func sectionObject(schema map[string]any, name string) any {
	for _, section := range schema["sections"].([]any) {
		value, exists := section.(map[string]any)[name]
		if exists {
			return value
		}
	}
	return nil
}

func schemaHash(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalSchemaV10(raw, DefaultSchemaLimits())
	if err != nil {
		t.Fatal(err)
	}
	return canonical.SHA256()
}

func TestActualRestartPayloadsAndImmutableResult(t *testing.T) {
	before, err := CanonicalSchemaV10(actualSchema(t, "schema-before.json"), DefaultSchemaLimits())
	if err != nil {
		t.Fatal(err)
	}
	after, err := CanonicalSchemaV10(actualSchema(t, "schema-after.json"), DefaultSchemaLimits())
	if err != nil || before.SHA256() != after.SHA256() || !bytes.Equal(before.Bytes(), after.Bytes()) {
		t.Fatal("same pinned program changed across restart", err)
	}
	if before.SHA256() != "f10d1efcbbdcec34434fa257946ca8a562ba1ad5f6b1b22b17fca14ca7eacc45" {
		t.Fatal("canonical byte contract changed", before.SHA256())
	}
	detached := before.Bytes()
	detached[0] = 'x'
	if before.Bytes()[0] != '{' || before.Version() != CanonicalizerVersion || before.Marker() != CanonicalizerVersion+"."+before.SHA256() {
		t.Fatal("borrowed output changed the observation")
	}
}

func TestOrderedParameterAndIndexLists(t *testing.T) {
	schema := schemaObject(t)
	reverseParameters(schema)
	before := schemaHash(t, schema)
	params := sectionObject(schema, "Reducers").([]any)[0].(map[string]any)["params"].(map[string]any)["elements"].([]any)
	slices.Reverse(params)
	if schemaHash(t, schema) == before {
		t.Fatal("parameter order was normalized away")
	}
	schema = schemaObject(t)
	changeIndexColumns(schema)
	before = schemaHash(t, schema)
	index := sectionObject(schema, "Tables").([]any)[0].(map[string]any)["indexes"].([]any)[0].(map[string]any)
	columns := index["algorithm"].(map[string]any)["BTree"].([]any)
	slices.Reverse(columns)
	if schemaHash(t, schema) == before {
		t.Fatal("index column order was normalized away")
	}
}

func TestDeclarationPermutationAndTypeRebinding(t *testing.T) {
	original := schemaObject(t)
	expected := schemaHash(t, original)
	for _, section := range []string{"Types", "Tables", "Reducers", "Schedules", "LifeCycleReducers"} {
		slices.Reverse(sectionObject(original, section).([]any))
	}
	slices.Reverse(sectionObject(original, "ExplicitNames").(map[string]any)["entries"].([]any))
	slices.Reverse(original["sections"].([]any))
	if schemaHash(t, original) != expected {
		t.Fatal("declaration order entered schema authority")
	}
	rebound := reindexSchema(schemaObject(t))
	if schemaHash(t, rebound) != expected {
		t.Fatal("correctly rebound typespace changed schema")
	}
	unbound := schemaObject(t)
	slices.Reverse(sectionObject(unbound, "Typespace").(map[string]any)["types"].([]any))
	if schemaHash(t, unbound) == expected {
		t.Fatal("unrebound references were ignored")
	}
}

func reindexSchema(schema map[string]any) map[string]any {
	types := sectionObject(schema, "Typespace").(map[string]any)["types"].([]any)
	count := len(types)
	rebindReferences(schema, count)
	slices.Reverse(types)
	for _, alias := range sectionObject(schema, "Types").([]any) {
		row := alias.(map[string]any)
		row["ty"] = reboundIndex(row["ty"], count)
	}
	for _, table := range sectionObject(schema, "Tables").([]any) {
		row := table.(map[string]any)
		row["product_type_ref"] = reboundIndex(row["product_type_ref"], count)
	}
	return schema
}

func reboundIndex(value any, count int) json.Number {
	index, _ := strconv.Atoi(string(value.(json.Number)))
	return json.Number(strconv.Itoa(count - index - 1))
}

func rebindReferences(value any, count int) {
	switch value := value.(type) {
	case map[string]any:
		if reference, present := value["Ref"]; present {
			value["Ref"] = reboundIndex(reference, count)
			return
		}
		for _, item := range value {
			rebindReferences(item, count)
		}
	case []any:
		for _, item := range value {
			rebindReferences(item, count)
		}
	}
}

func TestOrderedAndAccessChangesRemainDifferent(t *testing.T) {
	expected := schemaHash(t, schemaObject(t))
	for _, mutate := range []func(map[string]any){reverseColumns, reverseParameters, changeAccess, changeDefaults, changeIndexColumns} {
		value := schemaObject(t)
		mutate(value)
		if schemaHash(t, value) == expected {
			t.Fatal("meaningful schema change was discarded")
		}
	}
}

func reverseColumns(schema map[string]any) {
	types := sectionObject(schema, "Typespace").(map[string]any)["types"].([]any)
	row := types[1].(map[string]any)["Product"].(map[string]any)
	slices.Reverse(row["elements"].([]any))
}

func reverseParameters(schema map[string]any) {
	rows := sectionObject(schema, "Reducers").([]any)
	params := rows[0].(map[string]any)["params"].(map[string]any)
	params["elements"] = []any{
		map[string]any{"name": map[string]any{"some": "first"}, "algebraic_type": map[string]any{"U64": []any{}}},
		map[string]any{"name": map[string]any{"some": "second"}, "algebraic_type": map[string]any{"U8": []any{}}},
	}
}

func changeAccess(schema map[string]any) {
	sectionObject(schema, "Tables").([]any)[0].(map[string]any)["table_access"] = map[string]any{"Public": []any{}}
}

func changeDefaults(schema map[string]any) {
	table := sectionObject(schema, "Tables").([]any)[0].(map[string]any)
	table["default_values"] = []any{map[string]any{"col_id": json.Number("0"), "value": []any{json.Number("7")}}}
}

func changeIndexColumns(schema map[string]any) {
	table := sectionObject(schema, "Tables").([]any)[0].(map[string]any)
	index := table["indexes"].([]any)[0].(map[string]any)
	index["algorithm"] = map[string]any{"BTree": []any{json.Number("0"), json.Number("1")}}
}

func TestUnsupportedWireAndGraphRefuse(t *testing.T) {
	for _, raw := range []string{
		`{"sections":[],"future":true}`, `{"sections":[],"sections":[]}`, `{"sections":[{"Views":[]}]}`,
		`{"sections":[{"Typespace":{"types":[]}}, {"Typespace":{"types":[]}}]}`,
		`{"sections":[{"Typespace":{"types":[{"Ref":0}]}}]}`,
		`{"sections":[{"Typespace":{"types":[{"Ref":1}]}}]}`,
		`{"sections":[{"Typespace":{"types":[{"Ref":true}]}}]}`,
		`{"sections":[{"Typespace":{"types":[{"U64":null}]}}]}`,
		`{"sections":[{"Typespace":{"types":[{"Novel":[]}]}}]}`,
		`{"sections":[{"Typespace":{"types":[{"Ref":0.0}]}}]}`,
		`{"sections":[{"Typespace":{"types":[]}}]} {}`,
		`{"sections":[{"Typespace":{"types":[]}}, {"Types":[{"source_name":{"scope":[],"source_name":"\ud800"},"ty":0,"custom_ordering":true}]}]}`,
	} {
		if _, err := CanonicalSchemaV10([]byte(raw), DefaultSchemaLimits()); !errors.Is(err, ErrSchema) {
			t.Fatal("accepted unsupported schema", raw, err)
		}
	}
	value := schemaObject(t)
	aliases := sectionObject(value, "Types").([]any)
	aliases[1].(map[string]any)["source_name"] = aliases[0].(map[string]any)["source_name"]
	raw, _ := json.Marshal(value)
	if _, err := CanonicalSchemaV10(raw, DefaultSchemaLimits()); err == nil {
		t.Fatal("conflicting alias declarations accepted")
	}
}

func TestSchemaBudgetsAndCanonicalStringEncoding(t *testing.T) {
	raw := actualSchema(t, "schema-before.json")
	for _, limit := range []SchemaLimits{
		{MaxBytes: 64, MaxTypes: 1024, MaxNodes: 100_000, MaxDepth: 64, MaxDeclarations: 4096},
		{MaxBytes: 1 << 20, MaxTypes: 1, MaxNodes: 100_000, MaxDepth: 64, MaxDeclarations: 4096},
		{MaxBytes: 1 << 20, MaxTypes: 1024, MaxNodes: 20, MaxDepth: 64, MaxDeclarations: 4096},
		{MaxBytes: 1 << 20, MaxTypes: 1024, MaxNodes: 100_000, MaxDepth: 2, MaxDeclarations: 4096},
		{MaxBytes: 1 << 20, MaxTypes: 1024, MaxNodes: 100_000, MaxDepth: 64, MaxDeclarations: 1},
		{},
	} {
		if _, err := CanonicalSchemaV10(raw, limit); err == nil {
			t.Fatal("schema exceeded a declared budget")
		}
	}
	value := map[string]any{"text": "<>&\u2028\\\"\n\x00é", "number": json.Number("340282366920938463463374607431768211455")}
	encoded, err := encodeDocument(value, 1024)
	if err != nil || !json.Valid(encoded) || !bytes.Contains(encoded, []byte("<>&\u2028")) {
		t.Fatal("canonical UTF-8 JSON encoding", string(encoded), err)
	}
}
