// Package strictjson validates exact, required object fields at wire boundaries.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

var ErrObject = errors.New("invalid JSON object")

// Object rejects aliases, duplicates, missing fields and trailing values.
// Each caller owns typed validation of its field values and nested objects.
func Object(data []byte, fields ...string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(data) {
		return nil, ErrObject
	}
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, ErrObject
	}
	allowed := make(map[string]bool, len(fields))
	for _, name := range fields {
		allowed[name] = true
	}
	values := make(map[string]json.RawMessage, len(fields))
	for d.More() {
		token, err := d.Token()
		name, ok := token.(string)
		if err != nil || !ok || !allowed[name] {
			return nil, ErrObject
		}
		if _, exists := values[name]; exists {
			return nil, ErrObject
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return nil, ErrObject
		}
		values[name] = raw
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || len(values) != len(fields) {
		return nil, ErrObject
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrObject
	}
	return values, nil
}

func Value[T any](raw json.RawMessage) (T, error) {
	var value T
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return value, ErrObject
	}
	err := json.Unmarshal(raw, &value)
	if err != nil {
		return value, ErrObject
	}
	return value, nil
}
