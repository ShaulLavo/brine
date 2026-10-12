package spacetimedb

import "encoding/json"

func validateTypes(types []any) error {
	for _, value := range types {
		if err := validateType(value); err != nil {
			return err
		}
	}
	return nil
}

func validateType(value any) error {
	name, body, err := variant(value)
	if err != nil {
		return err
	}
	switch name {
	case "Ref":
		_, err = integer(body)
		return err
	case "Array":
		return validateType(body)
	case "Product":
		return validateProduct(body)
	case "Sum":
		object, err := exactObject(body, "variants")
		if err != nil {
			return err
		}
		return validateElements(object["variants"])
	case "Bool", "I8", "I16", "I32", "I64", "I128", "I256", "U8", "U16", "U32", "U64", "U128", "U256", "F32", "F64", "String":
		return emptyArray(body)
	default:
		return ErrSchema
	}
}

func validateProduct(value any) error {
	object, err := exactObject(value, "elements")
	if err != nil {
		return err
	}
	return validateElements(object["elements"])
}

func validateElements(value any) error {
	rows, ok := value.([]any)
	if !ok {
		return ErrSchema
	}
	names := map[string]bool{}
	for _, value := range rows {
		row, err := exactObject(value, "name", "algebraic_type")
		if err != nil || optionalName(row["name"]) != nil || validateType(row["algebraic_type"]) != nil {
			return ErrSchema
		}
		if err := uniqueElementName(row["name"], names); err != nil {
			return err
		}
	}
	return nil
}

func uniqueElementName(value any, names map[string]bool) error {
	variant, body, err := variant(value)
	if err != nil {
		return err
	}
	if variant == "none" {
		return nil
	}
	name := body.(string)
	if names[name] {
		return ErrSchema
	}
	names[name] = true
	return nil
}

func validateDeclaration(section string, value any) error {
	switch section {
	case "Types":
		return validateAlias(value)
	case "Tables":
		return validateTable(value)
	case "Reducers":
		return validateReducer(value)
	case "Schedules":
		return validateSchedule(value)
	case "LifeCycleReducers":
		return validateLifecycle(value)
	default:
		return ErrSchema
	}
}

func validateAlias(value any) error {
	row, err := exactObject(value, "source_name", "ty", "custom_ordering")
	if err != nil {
		return err
	}
	name, err := exactObject(row["source_name"], "scope", "source_name")
	if err != nil || identifier(name["source_name"]) != nil || identifierList(name["scope"]) != nil {
		return ErrSchema
	}
	if _, ok := row["custom_ordering"].(bool); !ok {
		return ErrSchema
	}
	_, err = integer(row["ty"])
	return err
}

func validateTable(value any) error {
	row, err := exactObject(value, "source_name", "product_type_ref", "primary_key", "indexes", "constraints", "sequences", "table_type", "table_access", "default_values", "is_event")
	if err != nil || identifier(row["source_name"]) != nil || integerList(row["primary_key"], 1<<31-1) != nil {
		return ErrSchema
	}
	if _, err = integer(row["product_type_ref"]); err != nil {
		return err
	}
	if _, ok := row["is_event"].(bool); !ok {
		return ErrSchema
	}
	if unitVariant(row["table_type"], "User", "System") != nil || unitVariant(row["table_access"], "Public", "Private") != nil {
		return ErrSchema
	}
	for _, field := range []string{"indexes", "constraints", "sequences", "default_values"} {
		if err := validateTableEntries(field, row[field]); err != nil {
			return err
		}
	}
	return nil
}

func validateTableEntries(kind string, value any) error {
	rows, ok := value.([]any)
	if !ok {
		return ErrSchema
	}
	for _, row := range rows {
		if err := validateTableEntry(kind, row); err != nil {
			return err
		}
	}
	return nil
}

func validateTableEntry(kind string, value any) error {
	switch kind {
	case "indexes":
		return validateIndex(value)
	case "constraints":
		return validateConstraint(value)
	case "sequences":
		return validateSequence(value)
	case "default_values":
		row, err := exactObject(value, "col_id", "value")
		if err != nil || hexByteString(row["value"]) != nil {
			return ErrSchema
		}
		_, err = integer(row["col_id"])
		return err
	default:
		return ErrSchema
	}
}

func hexByteString(value any) error {
	text, ok := value.(string)
	if !ok || len(text)%2 != 0 {
		return ErrSchema
	}
	for _, digit := range text {
		if digit >= '0' && digit <= '9' || digit >= 'a' && digit <= 'f' {
			continue
		}
		return ErrSchema
	}
	return nil
}

func validateIndex(value any) error {
	row, err := exactObject(value, "source_name", "accessor_name", "algorithm")
	if err != nil || optionalName(row["source_name"]) != nil || optionalName(row["accessor_name"]) != nil {
		return ErrSchema
	}
	name, columns, err := variant(row["algorithm"])
	if err != nil || name != "BTree" {
		return ErrSchema
	}
	return integerList(columns, 1<<31-1)
}

func validateConstraint(value any) error {
	row, err := exactObject(value, "source_name", "data")
	if err != nil || optionalName(row["source_name"]) != nil {
		return ErrSchema
	}
	body, err := exactObject(row["data"], "Unique")
	if err != nil {
		return err
	}
	columns, err := exactObject(body["Unique"], "columns")
	if err != nil {
		return err
	}
	return integerList(columns["columns"], 1<<31-1)
}

func validateSequence(value any) error {
	row, err := exactObject(value, "source_name", "column", "start", "min_value", "max_value", "increment")
	if err != nil || optionalName(row["source_name"]) != nil {
		return ErrSchema
	}
	if _, err = integer(row["column"]); err != nil {
		return err
	}
	for _, field := range []string{"start", "min_value", "max_value"} {
		if unitVariant(row[field], "none") != nil {
			return ErrSchema
		}
	}
	if row["increment"] != json.Number("1") {
		return ErrSchema
	}
	return nil
}

func validateReducer(value any) error {
	row, err := exactObject(value, "source_name", "params", "visibility", "ok_return_type", "err_return_type")
	if err != nil || identifier(row["source_name"]) != nil || validateProduct(row["params"]) != nil {
		return ErrSchema
	}
	if unitVariant(row["visibility"], "Private", "ClientCallable") != nil {
		return ErrSchema
	}
	if validateType(row["ok_return_type"]) != nil || validateType(row["err_return_type"]) != nil {
		return ErrSchema
	}
	return nil
}

func validateSchedule(value any) error {
	row, err := exactObject(value, "source_name", "table_name", "schedule_at_col", "function_name")
	if err != nil || optionalName(row["source_name"]) != nil || identifier(row["table_name"]) != nil || identifier(row["function_name"]) != nil {
		return ErrSchema
	}
	_, err = integer(row["schedule_at_col"])
	return err
}

func validateLifecycle(value any) error {
	row, err := exactObject(value, "lifecycle_spec", "function_name")
	if err != nil || identifier(row["function_name"]) != nil {
		return ErrSchema
	}
	return unitVariant(row["lifecycle_spec"], "Init", "OnConnect", "OnDisconnect")
}

func validateNameMapping(value any) error {
	name, body, err := variant(value)
	if err != nil || (name != "Table" && name != "Function" && name != "Index" && name != "Namespace") {
		return ErrSchema
	}
	row, err := exactObject(body, "source_name", "canonical_name")
	if err != nil || identifier(row["source_name"]) != nil || identifier(row["canonical_name"]) != nil {
		return ErrSchema
	}
	return nil
}

func optionalName(value any) error {
	name, body, err := variant(value)
	if err != nil {
		return err
	}
	if name == "none" {
		return emptyArray(body)
	}
	if name != "some" {
		return ErrSchema
	}
	return identifier(body)
}

func identifier(value any) error {
	text, ok := value.(string)
	if !ok || len(text) == 0 || len(text) > 256 {
		return ErrSchema
	}
	return nil
}

func identifierList(value any) error {
	items, ok := value.([]any)
	if !ok {
		return ErrSchema
	}
	for _, item := range items {
		if err := identifier(item); err != nil {
			return err
		}
	}
	return nil
}

func unitVariant(value any, allowed ...string) error {
	name, body, err := variant(value)
	if err != nil {
		return err
	}
	for _, candidate := range allowed {
		if name == candidate {
			return emptyArray(body)
		}
	}
	return ErrSchema
}

func emptyArray(value any) error {
	array, ok := value.([]any)
	if !ok || len(array) != 0 {
		return ErrSchema
	}
	return nil
}

func integerList(value any, max int) error {
	values, ok := value.([]any)
	if !ok {
		return ErrSchema
	}
	for _, value := range values {
		number, err := integer(value)
		if err != nil || number > max {
			return ErrSchema
		}
	}
	return nil
}
