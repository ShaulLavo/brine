package spacetimedb

import "reflect"

func aliasRows(section any) map[string]map[string]any {
	result := map[string]map[string]any{}
	rows, _ := section.([]any)
	for _, value := range rows {
		row := value.(map[string]any)
		key, _ := encodeDocument(row["source_name"], DefaultSchemaLimits().MaxBytes)
		result[string(key)] = row
	}
	return result
}

func (d *SchemaDiff) aliases(old, next map[string]any) {
	previous, current := aliasRows(old["Types"]), aliasRows(next["Types"])
	for _, name := range rowNames(previous) {
		source, target := previous[name], current[name]
		if reflect.DeepEqual(source, target) {
			continue
		}
		if target != nil && source["custom_ordering"] == target["custom_ordering"] && admittedProductAppend(source["ty"], target["ty"], old, next) {
			continue
		}
		d.change(SchemaChange{Kind: "exported_type_changed", Section: "Types", Declaration: name}, OperatorProcedureRequired, "")
	}
}

func (d *SchemaDiff) typespace(old, next map[string]any) {
	previous := old["Typespace"].(map[string]any)["types"].([]any)
	current := next["Typespace"].(map[string]any)["types"].([]any)
	for _, value := range previous {
		if containsValue(current, value) || replacedByProductAppend(value, current, old, next) {
			continue
		}
		d.change(SchemaChange{Kind: "type_removed_or_changed", Section: "Typespace"}, OperatorProcedureRequired, "")
		return
	}
}

func replacedByProductAppend(old any, types []any, previous, current map[string]any) bool {
	for _, next := range types {
		if admittedProductAppend(old, next, previous, current) {
			return true
		}
	}
	return false
}
