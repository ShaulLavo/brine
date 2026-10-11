package spacetimedb

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
)

var qualifiedSections = []string{"Typespace", "Types", "Tables", "Reducers", "Schedules", "LifeCycleReducers", "ExplicitNames"}

func exactObject(value any, fields ...string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok || len(object) != len(fields) {
		return nil, ErrSchema
	}
	for _, name := range fields {
		if _, present := object[name]; !present {
			return nil, ErrSchema
		}
	}
	return object, nil
}

func variant(value any) (string, any, error) {
	object, ok := value.(map[string]any)
	if !ok || len(object) != 1 {
		return "", nil, ErrSchema
	}
	for name, value := range object {
		return name, value, nil
	}
	return "", nil, ErrSchema
}

func decodeSections(value any) (map[string]any, error) {
	root, err := exactObject(value, "sections")
	if err != nil {
		return nil, err
	}
	sections, ok := root["sections"].([]any)
	if !ok || len(sections) > len(qualifiedSections) {
		return nil, ErrSchema
	}
	result := make(map[string]any, len(sections))
	for _, section := range sections {
		name, value, err := variant(section)
		if err != nil || !slices.Contains(qualifiedSections, name) {
			return nil, ErrSchema
		}
		if _, duplicate := result[name]; duplicate {
			return nil, ErrSchema
		}
		result[name] = value
	}
	return result, nil
}

type typeExpansion struct {
	types        []any
	limits       SchemaLimits
	nodes        int
	declarations int
}

func normalizeSections(raw map[string]any, limits SchemaLimits) (map[string]any, error) {
	space, err := exactObject(raw["Typespace"], "types")
	if err != nil {
		return nil, err
	}
	types, ok := space["types"].([]any)
	if !ok || len(types) > limits.MaxTypes {
		return nil, ErrSchema
	}
	expansion := typeExpansion{types: types, limits: limits}
	if err := validateTypes(types); err != nil {
		return nil, err
	}
	result := make(map[string]any, len(raw))
	for name, value := range raw {
		normalized, err := expansion.section(name, value)
		if err != nil {
			return nil, err
		}
		result[name] = normalized
	}
	return result, nil
}

func (e *typeExpansion) reference(value any, ancestors []int) (any, error) {
	index, err := integer(value)
	if err != nil || index >= len(e.types) || slices.Contains(ancestors, index) || len(ancestors) >= e.limits.MaxDepth {
		return nil, ErrSchema
	}
	path := append(slices.Clone(ancestors), index)
	return e.walk(e.types[index], path, 0)
}

func (e *typeExpansion) walk(value any, ancestors []int, depth int) (any, error) {
	e.nodes++
	if e.nodes > e.limits.MaxNodes || depth > e.limits.MaxDepth {
		return nil, ErrSchema
	}
	switch value := value.(type) {
	case map[string]any:
		return e.walkObject(value, ancestors, depth+1)
	case []any:
		return e.walkArray(value, ancestors, depth+1)
	default:
		return value, nil
	}
}

func (e *typeExpansion) walkArray(value []any, ancestors []int, depth int) (any, error) {
	result := make([]any, len(value))
	for index, item := range value {
		expanded, err := e.walk(item, ancestors, depth)
		if err != nil {
			return nil, err
		}
		result[index] = expanded
	}
	return result, nil
}

func (e *typeExpansion) walkObject(value map[string]any, ancestors []int, depth int) (any, error) {
	if reference, exists := value["Ref"]; exists {
		if len(value) != 1 {
			return nil, ErrSchema
		}
		return e.reference(reference, ancestors)
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		expanded, err := e.walk(item, ancestors, depth)
		if err != nil {
			return nil, err
		}
		result[key] = expanded
	}
	return result, nil
}

func (e *typeExpansion) section(name string, value any) (any, error) {
	if name == "Typespace" {
		return e.typespace()
	}
	if name == "ExplicitNames" {
		return e.explicitNames(value)
	}
	rows, ok := value.([]any)
	e.declarations += len(rows)
	if !ok || e.declarations > e.limits.MaxDeclarations {
		return nil, ErrSchema
	}
	result := make([]any, 0, len(rows))
	for _, row := range rows {
		if err := validateDeclaration(name, row); err != nil {
			return nil, err
		}
		expanded, err := e.declaration(name, row)
		if err != nil {
			return nil, err
		}
		result = append(result, expanded)
	}
	return sortDeclarations(result, declarationKey(name), false, e.limits.MaxBytes)
}

func (e *typeExpansion) typespace() (any, error) {
	result := make([]any, 0, len(e.types))
	for index := range e.types {
		typeValue, err := e.reference(json.Number(strconv.Itoa(index)), nil)
		if err != nil {
			return nil, err
		}
		result = append(result, typeValue)
	}
	values, err := sortDeclarations(result, "", true, e.limits.MaxBytes)
	if err != nil {
		return nil, err
	}
	return map[string]any{"types": values}, nil
}

func (e *typeExpansion) declaration(name string, row any) (any, error) {
	expanded, err := e.walk(row, nil, 0)
	if err != nil {
		return nil, err
	}
	object := expanded.(map[string]any)
	if name == "Types" {
		object["ty"], err = e.reference(object["ty"], nil)
		return object, err
	}
	if name != "Tables" {
		return object, nil
	}
	object["product_type"], err = e.reference(object["product_type_ref"], nil)
	if err != nil {
		return nil, err
	}
	product, err := exactObject(object["product_type"], "Product")
	if err != nil || product["Product"] == nil {
		return nil, ErrSchema
	}
	if err := validateTableColumns(object, product["Product"]); err != nil {
		return nil, err
	}
	delete(object, "product_type_ref")
	for _, field := range []string{"indexes", "constraints", "sequences", "default_values"} {
		key := "source_name"
		if field == "default_values" {
			key = "col_id"
		}
		object[field], err = sortDeclarations(object[field].([]any), key, false, e.limits.MaxBytes)
		if err != nil {
			return nil, err
		}
	}
	return object, nil
}

func (e *typeExpansion) explicitNames(value any) (any, error) {
	object, err := exactObject(value, "entries")
	if err != nil {
		return nil, err
	}
	rows, ok := object["entries"].([]any)
	e.declarations += len(rows)
	if !ok || e.declarations > e.limits.MaxDeclarations {
		return nil, ErrSchema
	}
	for _, row := range rows {
		if err := validateNameMapping(row); err != nil {
			return nil, err
		}
	}
	if err := uniqueNameMappings(rows, e.limits.MaxBytes); err != nil {
		return nil, err
	}
	result, err := sortDeclarations(rows, "", false, e.limits.MaxBytes)
	return map[string]any{"entries": result}, err
}

func uniqueNameMappings(rows []any, limit int) error {
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		name, value, _ := variant(row)
		mapping := value.(map[string]any)
		key, err := encodeDocument(map[string]any{name: mapping["source_name"]}, limit)
		if err != nil || seen[string(key)] {
			return ErrSchema
		}
		seen[string(key)] = true
	}
	return nil
}

type sortableValue struct {
	value any
	raw   []byte
}

func sortDeclarations(values []any, key string, allowDuplicate bool, limit int) ([]any, error) {
	rows := make([]sortableValue, 0, len(values))
	keys := make(map[string]bool, len(values))
	for _, value := range values {
		raw, err := encodeDocument(value, limit)
		if err != nil {
			return nil, err
		}
		identity := raw
		if key != "" {
			identity, err = encodeDocument(value.(map[string]any)[key], limit)
		}
		if err != nil || (!allowDuplicate && keys[string(identity)]) {
			return nil, ErrSchema
		}
		keys[string(identity)] = true
		rows = append(rows, sortableValue{value: value, raw: raw})
	}
	slices.SortFunc(rows, func(a, b sortableValue) int { return bytes.Compare(a.raw, b.raw) })
	result := make([]any, len(rows))
	for index, row := range rows {
		result[index] = row.value
	}
	return result, nil
}

func declarationKey(section string) string {
	if section == "LifeCycleReducers" {
		return "lifecycle_spec"
	}
	return "source_name"
}

func integer(value any) (int, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, ErrSchema
	}
	parsed, err := strconv.ParseUint(string(number), 10, 31)
	if err != nil {
		return 0, ErrSchema
	}
	return int(parsed), nil
}
