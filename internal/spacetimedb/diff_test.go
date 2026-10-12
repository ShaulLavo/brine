package spacetimedb

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestActualSchemaDiffsRequireIndependentVerification(t *testing.T) {
	for _, test := range []struct {
		variant     string
		decision    SchemaDecision
		requirement SchemaRequirement
	}{
		{"v1", StagedValidationRequired, ReviewGuestCode},
		{"additive", StagedValidationRequired, VerifyNewTableData},
		{"default-column", StagedValidationRequired, VerifyDefaultProjection},
		{"destructive", OperatorProcedureRequired, VerifyPreservedData},
		{"access-change", OperatorProcedureRequired, VerifyPreservedData},
		{"observer-hooks", StagedValidationRequired, QualifyLifecycleEffects},
	} {
		t.Run(test.variant, func(t *testing.T) {
			assertActualDiff(t, test.variant, test.decision, test.requirement)
		})
	}
}

func assertActualDiff(t *testing.T, variant string, decision SchemaDecision, requirement SchemaRequirement) {
	t.Helper()
	diff, err := CompareSchemas(descriptorSchema(t, "v1"), descriptorSchema(t, variant))
	if err != nil || diff.Decision() != decision || !slices.Contains(diff.Requirements(), requirement) || !slices.Contains(diff.Requirements(), ReviewGuestCode) {
		t.Fatal("actual candidate lost an independent admission requirement", variant, diff.Decision(), diff.Changes(), diff.Requirements(), err)
	}
}

func TestNativeU8DefaultsAreTypedAndProjected(t *testing.T) {
	diff, err := CompareSchemas(descriptorSchema(t, "v1"), descriptorSchema(t, "default-column"))
	if err != nil {
		t.Fatal(err)
	}
	added := 0
	for _, change := range diff.Changes() {
		if change.Kind != "defaulted_column_appended" {
			continue
		}
		added++
		if change.Field != "rating" || change.Column != 4 || change.DefaultHex != "07" {
			t.Fatal("native default byte/type/position changed", change)
		}
	}
	if added != 2 || diff.Decision() != StagedValidationRequired {
		t.Fatal("default append was promoted or lost", diff.Changes())
	}
}

func TestDiffRetainsMandatoryRequirementsAndOwnsResults(t *testing.T) {
	diff, err := CompareSchemas(descriptorSchema(t, "v1"), descriptorSchema(t, "destructive"))
	if err != nil {
		t.Fatal(err)
	}
	changes := diff.Changes()
	changes[0].Kind = "safe"
	requirements := diff.Requirements()
	requirements[0] = "reviewed"
	if diff.Decision() != OperatorProcedureRequired || diff.Changes()[0].Kind == "safe" || !slices.Contains(diff.Requirements(), ReviewGuestCode) {
		t.Fatal("caller downgraded retained schema decision")
	}
}

func TestOrderedProductsAndExistingConstraintsCannotBeDowngraded(t *testing.T) {
	for _, mutate := range []func(map[string]any){reverseColumns, changeAccess, changeDefaults, changeIndexColumns, changePrimaryKey} {
		schema := schemaObject(t)
		mutate(schema)
		assertChangedTableRequiresProcedure(t, schema)
	}
}

func TestImplicitExistingNameCannotBecomeReviewedAddition(t *testing.T) {
	for _, kind := range []string{"Table", "Function"} {
		t.Run(kind, func(t *testing.T) { assertImplicitNameRequiresProcedure(t, kind) })
	}
}

func assertImplicitNameRequiresProcedure(t *testing.T, kind string) {
	t.Helper()
	source := schemaObject(t)
	names := sectionObject(source, "ExplicitNames").(map[string]any)
	entries := names["entries"].([]any)
	index := slices.IndexFunc(entries, func(entry any) bool {
		entryKind, _, _ := variant(entry)
		return entryKind == kind
	})
	if index < 0 {
		t.Fatal("native fixture has no explicit mapping", kind)
	}
	_, value, _ := variant(entries[index])
	mapping := value.(map[string]any)
	name := mapping["source_name"].(string)
	names["entries"] = slices.Delete(entries, index, index+1)
	before := mustSchemaValue(t, source)
	names["entries"] = append(names["entries"].([]any), map[string]any{kind: map[string]any{"source_name": name, "canonical_name": "renamed_existing"}})
	after := mustSchemaValue(t, source)
	diff, err := CompareSchemas(before, after)
	if err != nil || diff.Decision() != OperatorProcedureRequired {
		t.Fatal("existing implicit name gained unreviewed alias", diff.Decision(), diff.Changes(), err)
	}
}

func TestExistingNativeIndexNameCannotBecomeReviewedAddition(t *testing.T) {
	source := descriptorSchema(t, "v1")
	if source.SHA256() != "f10d1efcbbdcec34434fa257946ca8a562ba1ad5f6b1b22b17fca14ca7eacc45" {
		t.Fatal("source is not the unchanged native baseline")
	}
	next := schemaObject(t)
	names := sectionObject(next, "ExplicitNames").(map[string]any)
	names["entries"] = append(names["entries"].([]any), map[string]any{"Index": map[string]any{
		"source_name": "recovery_left_sequence_idx_btree", "canonical_name": "renamed_existing_index",
	}})
	diff, err := CompareSchemas(source, mustSchemaValue(t, next))
	if err != nil || diff.Decision() != OperatorProcedureRequired {
		t.Fatal("existing native index gained unreviewed alias", diff.Decision(), diff.Changes(), err)
	}
}

func mustSchemaValue(t *testing.T, value any) CanonicalSchema {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := CanonicalSchemaV10(raw, DefaultSchemaLimits())
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func changePrimaryKey(schema map[string]any) {
	table := sectionObject(schema, "Tables").([]any)[0].(map[string]any)
	table["primary_key"] = []any{json.Number("1")}
}

func assertChangedTableRequiresProcedure(t *testing.T, schema map[string]any) {
	t.Helper()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := CanonicalSchemaV10(raw, DefaultSchemaLimits())
	if err != nil {
		t.Fatal(err)
	}
	diff, err := CompareSchemas(mustNativeSchema(t), changed)
	if err != nil || diff.Decision() != OperatorProcedureRequired {
		t.Fatal("existing persistent definition change downgraded", diff.Decision(), diff.Changes(), err)
	}
}
