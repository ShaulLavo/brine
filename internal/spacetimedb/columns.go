package spacetimedb

func validateTableColumns(table map[string]any, product any) error {
	fields := product.(map[string]any)["elements"].([]any)
	for _, field := range fields {
		name, _, _ := variant(field.(map[string]any)["name"])
		if name != "some" {
			return ErrSchema
		}
	}
	arity := len(fields)
	primary := table["primary_key"].([]any)
	if len(primary) > 1 || columnList(primary, arity, true) != nil {
		return ErrSchema
	}
	for _, kind := range []string{"indexes", "constraints", "sequences", "default_values"} {
		if err := tableColumnEntries(kind, table[kind].([]any), arity); err != nil {
			return err
		}
	}
	return nil
}

func tableColumnEntries(kind string, rows []any, arity int) error {
	for _, row := range rows {
		if err := tableColumnEntry(kind, row.(map[string]any), arity); err != nil {
			return err
		}
	}
	return nil
}

func tableColumnEntry(kind string, row map[string]any, arity int) error {
	switch kind {
	case "indexes":
		return columnList(row["algorithm"].(map[string]any)["BTree"].([]any), arity, false)
	case "constraints":
		unique := row["data"].(map[string]any)["Unique"].(map[string]any)
		return columnList(unique["columns"].([]any), arity, false)
	case "sequences":
		return columnIndex(row["column"], arity)
	case "default_values":
		return columnIndex(row["col_id"], arity)
	default:
		return ErrSchema
	}
}

func columnList(columns []any, arity int, allowEmpty bool) error {
	if !allowEmpty && len(columns) == 0 {
		return ErrSchema
	}
	seen := make(map[int]bool, len(columns))
	for _, value := range columns {
		index, err := integer(value)
		if err != nil || index >= arity || seen[index] {
			return ErrSchema
		}
		seen[index] = true
	}
	return nil
}

func columnIndex(value any, arity int) error {
	index, err := integer(value)
	if err != nil || index >= arity {
		return ErrSchema
	}
	return nil
}
