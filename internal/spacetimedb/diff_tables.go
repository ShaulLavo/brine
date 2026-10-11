package spacetimedb

import "reflect"

func (d *SchemaDiff) tables(old, next map[string]any) {
	previous, current := namedRows(old["Tables"]), namedRows(next["Tables"])
	for _, name := range rowNames(previous) {
		target, present := current[name]
		if !present {
			d.change(SchemaChange{Kind: "table_removed", Section: "Tables", Declaration: name}, OperatorProcedureRequired, "")
			continue
		}
		d.table(previous[name], target)
	}
	for _, name := range rowNames(current) {
		if previous[name] != nil {
			continue
		}
		d.change(SchemaChange{Kind: "table_added", Section: "Tables", Declaration: name}, StagedValidationRequired, VerifyNewTableData)
	}
}

func (d *SchemaDiff) table(old, next map[string]any) {
	name := old["source_name"].(string)
	for _, property := range []string{"primary_key", "indexes", "constraints", "sequences", "table_access", "table_type", "is_event"} {
		if reflect.DeepEqual(old[property], next[property]) {
			continue
		}
		d.change(SchemaChange{Kind: "table_property_changed", Section: "Tables", Declaration: name, Field: property}, OperatorProcedureRequired, "")
	}
	previous, current := productFields(old), productFields(next)
	if !orderedPrefix(previous, current) {
		d.change(SchemaChange{Kind: "ordered_product_changed", Section: "Tables", Declaration: name}, OperatorProcedureRequired, "")
		return
	}
	if !existingDefaultsPreserved(old, next, len(previous)) {
		d.change(SchemaChange{Kind: "existing_defaults_changed", Section: "Tables", Declaration: name}, OperatorProcedureRequired, "")
	}
	d.appendedColumns(next, len(previous))
}

func productFields(table map[string]any) []any {
	product := table["product_type"].(map[string]any)["Product"].(map[string]any)
	return product["elements"].([]any)
}

func orderedPrefix(old, next []any) bool {
	return len(next) >= len(old) && reflect.DeepEqual(old, next[:len(old)])
}

func defaultColumns(table map[string]any) map[int]string {
	result := map[int]string{}
	for _, value := range table["default_values"].([]any) {
		row := value.(map[string]any)
		index, _ := integer(row["col_id"])
		result[index] = row["value"].(string)
	}
	return result
}

func existingDefaultsPreserved(old, next map[string]any, columns int) bool {
	previous, current := defaultColumns(old), defaultColumns(next)
	for column := range columns {
		before, oldPresent := previous[column]
		after, newPresent := current[column]
		if oldPresent != newPresent || before != after {
			return false
		}
	}
	return true
}

func (d *SchemaDiff) appendedColumns(table map[string]any, first int) {
	defaults := defaultColumns(table)
	fields := productFields(table)
	for column := first; column < len(fields); column++ {
		field := fields[column].(map[string]any)
		_, name, _ := variant(field["name"])
		value, present := defaults[column]
		change := SchemaChange{Kind: "defaulted_column_appended", Section: "Tables", Declaration: table["source_name"].(string), Field: name.(string), Column: column, DefaultHex: value}
		if !present || !qualifiedU8Default(field["algebraic_type"], value) {
			change.Kind = "unqualified_column_append"
			d.change(change, UnsupportedSchemaChange, "")
			continue
		}
		d.change(change, StagedValidationRequired, VerifyDefaultProjection)
	}
}

func qualifiedU8Default(kind any, value string) bool {
	name, body, err := variant(kind)
	return err == nil && name == "U8" && emptyArray(body) == nil && len(value) == 2 && hexByteString(value) == nil
}

func admittedProductAppend(old, next any, previous, current map[string]any) bool {
	oldTables, newTables := namedRows(previous["Tables"]), namedRows(current["Tables"])
	for name, source := range oldTables {
		target := newTables[name]
		if target == nil || !reflect.DeepEqual(old, source["product_type"]) || !reflect.DeepEqual(next, target["product_type"]) {
			continue
		}
		if qualifiedProductAppend(source, target) {
			return true
		}
	}
	return false
}

func qualifiedProductAppend(old, next map[string]any) bool {
	previous, current := productFields(old), productFields(next)
	if len(current) <= len(previous) || !orderedPrefix(previous, current) || !existingDefaultsPreserved(old, next, len(previous)) {
		return false
	}
	defaults := defaultColumns(next)
	for column := len(previous); column < len(current); column++ {
		field := current[column].(map[string]any)
		value, present := defaults[column]
		if !present || !qualifiedU8Default(field["algebraic_type"], value) {
			return false
		}
	}
	return true
}
