package spacetimedb

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
)

type SchemaDecision string

const (
	StagedValidationRequired  SchemaDecision = "staged_validation_required"
	OperatorProcedureRequired SchemaDecision = "operator_procedure_required"
	UnsupportedSchemaChange   SchemaDecision = "unsupported_schema_change"
)

type SchemaRequirement string

const (
	ReviewGuestCode         SchemaRequirement = "review_guest_code"
	VerifyPreservedData     SchemaRequirement = "verify_preserved_data"
	VerifyNewTableData      SchemaRequirement = "verify_new_table_data"
	VerifyDefaultProjection SchemaRequirement = "verify_default_projection"
	QualifyCallableEffects  SchemaRequirement = "qualify_callable_effects"
	QualifyLifecycleEffects SchemaRequirement = "qualify_lifecycle_effects"
	QualifySchedulerEffects SchemaRequirement = "qualify_scheduler_effects"
)

type SchemaChange struct {
	Kind        string `json:"kind"`
	Section     string `json:"section"`
	Declaration string `json:"declaration"`
	Field       string `json:"field,omitempty"`
	Column      int    `json:"column,omitempty"`
	DefaultHex  string `json:"default_hex,omitempty"`
}

// SchemaDiff describes required checks. It grants no publication authority.
type SchemaDiff struct {
	source       string
	target       string
	decision     SchemaDecision
	changes      []SchemaChange
	requirements []SchemaRequirement
}

func (d SchemaDiff) SourceSHA256() string              { return d.source }
func (d SchemaDiff) TargetSHA256() string              { return d.target }
func (d SchemaDiff) Decision() SchemaDecision          { return d.decision }
func (d SchemaDiff) Changes() []SchemaChange           { return slices.Clone(d.changes) }
func (d SchemaDiff) Requirements() []SchemaRequirement { return slices.Clone(d.requirements) }

func CompareSchemas(source, target CanonicalSchema) (SchemaDiff, error) {
	old, err := canonicalView(source)
	if err != nil {
		return SchemaDiff{}, err
	}
	next, err := canonicalView(target)
	if err != nil {
		return SchemaDiff{}, err
	}
	diff := SchemaDiff{source: source.SHA256(), target: target.SHA256(), decision: StagedValidationRequired,
		changes: []SchemaChange{}, requirements: []SchemaRequirement{ReviewGuestCode, VerifyPreservedData}}
	diff.tables(old, next)
	diff.aliases(old, next)
	diff.typespace(old, next)
	diff.effects(old, next)
	return diff, nil
}

func canonicalView(schema CanonicalSchema) (map[string]any, error) {
	if schema.hash == "" {
		return nil, ErrSchema
	}
	decoder := json.NewDecoder(bytes.NewReader(schema.document))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, ErrSchema
	}
	return document["sections"].(map[string]any), nil
}

func namedRows(section any) map[string]map[string]any {
	result := map[string]map[string]any{}
	rows, _ := section.([]any)
	for _, value := range rows {
		row := value.(map[string]any)
		result[row["source_name"].(string)] = row
	}
	return result
}

func rowNames(rows map[string]map[string]any) []string {
	names := make([]string, 0, len(rows))
	for name := range rows {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (d *SchemaDiff) change(change SchemaChange, decision SchemaDecision, requirement SchemaRequirement) {
	d.changes = append(d.changes, change)
	if decision == UnsupportedSchemaChange || decision == OperatorProcedureRequired && d.decision == StagedValidationRequired {
		d.decision = decision
	}
	if requirement != "" && !slices.Contains(d.requirements, requirement) {
		d.requirements = append(d.requirements, requirement)
	}
}

func (d *SchemaDiff) effects(old, next map[string]any) {
	for _, section := range []struct {
		name        string
		requirement SchemaRequirement
	}{
		{"Reducers", QualifyCallableEffects}, {"LifeCycleReducers", QualifyLifecycleEffects}, {"Schedules", QualifySchedulerEffects},
	} {
		if reflect.DeepEqual(old[section.name], next[section.name]) {
			continue
		}
		d.change(SchemaChange{Kind: "effects_changed", Section: section.name}, StagedValidationRequired, section.requirement)
	}
	if !reflect.DeepEqual(old["ExplicitNames"], next["ExplicitNames"]) && !explicitNamesOnlyAdded(old["ExplicitNames"], next["ExplicitNames"]) {
		d.change(SchemaChange{Kind: "explicit_names_changed", Section: "ExplicitNames"}, OperatorProcedureRequired, "")
	}
}

func explicitNamesOnlyAdded(old, next any) bool {
	previous, oldOK := old.(map[string]any)
	current, nextOK := next.(map[string]any)
	if !oldOK || !nextOK {
		return false
	}
	for _, entry := range previous["entries"].([]any) {
		if !containsValue(current["entries"].([]any), entry) {
			return false
		}
	}
	return true
}

func containsValue(values []any, expected any) bool {
	return slices.ContainsFunc(values, func(value any) bool { return reflect.DeepEqual(value, expected) })
}
