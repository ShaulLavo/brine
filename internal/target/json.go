package target

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

// Decode validates the wire schema without refusing unsupported host platforms.
// Inventory must be able to describe such hosts; planners call Validate separately.
func Decode(data []byte) (Snapshot, error) {
	var s Snapshot
	if !utf8.Valid(data) {
		return s, fmt.Errorf("snapshot JSON: invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := checkJSON(decoder, reflect.TypeFor[Snapshot]()); err != nil {
		return s, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return s, fmt.Errorf("snapshot: expected one JSON object")
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Snapshot{}, fmt.Errorf("snapshot: %w", err)
	}
	if err := s.validateShape(); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// checkJSON adds exact field names, required fields, duplicate-key and null
// rejection to encoding/json's typed decoding. The Go tags own the wire schema.
func checkJSON(d *json.Decoder, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	token, err := d.Token()
	if err != nil {
		return fmt.Errorf("snapshot JSON: %w", err)
	}
	if token == nil {
		return fmt.Errorf("snapshot JSON: null is not an observation")
	}
	switch t.Kind() {
	case reflect.Struct:
		if token != json.Delim('{') {
			return fmt.Errorf("snapshot JSON: expected object")
		}
		fields := map[string]reflect.StructField{}
		var required []string
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")
			fields[tag[0]] = field
			if len(tag) == 1 {
				required = append(required, tag[0])
			}
		}
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("snapshot JSON: expected field name")
			}
			field, ok := fields[name]
			if !ok {
				return fmt.Errorf("snapshot JSON: unknown field")
			}
			if seen[name] {
				return fmt.Errorf("snapshot JSON: duplicate field")
			}
			seen[name] = true
			if err := checkJSON(d, field.Type); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
		for _, name := range required {
			if !seen[name] {
				return fmt.Errorf("%s: missing required field", name)
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("snapshot JSON: unclosed object")
		}
	case reflect.Slice:
		if token != json.Delim('[') {
			return fmt.Errorf("snapshot JSON: expected array")
		}
		for d.More() {
			if err := checkJSON(d, t.Elem()); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("snapshot JSON: unclosed array")
		}
	default:
		if _, ok := token.(json.Delim); ok {
			return fmt.Errorf("snapshot JSON: expected scalar")
		}
	}
	return nil
}

// Encode returns compact canonical JSON with one trailing newline. Sets are
// sorted on copies so hashing a snapshot cannot reorder inventory's slices.
func Encode(s Snapshot) ([]byte, error) {
	if err := s.validateShape(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var canonical Snapshot
	if err := json.Unmarshal(raw, &canonical); err != nil {
		return nil, err
	}
	if canonical.UsedPorts.Value != nil {
		slices.Sort(*canonical.UsedPorts.Value)
	}
	if canonical.CaddyConfig.Value != nil {
		slices.SortFunc(canonical.CaddyConfig.Value.Files, func(a, b CaddyFile) int { return strings.Compare(a.Name, b.Name) })
	}
	if canonical.Apps.Value != nil {
		apps := *canonical.Apps.Value
		slices.SortFunc(apps, func(a, b App) int { return strings.Compare(a.Name, b.Name) })
		for _, app := range apps {
			if app.QuadletUnits.Value != nil {
				slices.SortFunc(*app.QuadletUnits.Value, func(a, b Unit) int { return strings.Compare(a.Name, b.Name) })
			}
			if app.Secrets.Value != nil {
				slices.SortFunc(*app.Secrets.Value, func(a, b Secret) int { return strings.Compare(a.Name, b.Name) })
			}
		}
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
