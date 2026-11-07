package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"unicode/utf8"
)

func decodeDecisionDocument(raw []byte, document any) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("invalid decision input document")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	source, err := decisionJSONValue(d)
	if err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("invalid decision input document")
	}
	typed := json.NewDecoder(bytes.NewReader(raw))
	typed.DisallowUnknownFields()
	if err := typed.Decode(document); err != nil {
		return err
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return err
	}
	d = json.NewDecoder(bytes.NewReader(canonical))
	d.UseNumber()
	var normalized any
	if err := d.Decode(&normalized); err != nil || !reflect.DeepEqual(source, normalized) {
		return fmt.Errorf("invalid decision input fields")
	}
	return nil
}

func decisionJSONValue(d *json.Decoder) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		return decisionJSONObject(d)
	case json.Delim('['):
		return decisionJSONArray(d)
	default:
		return token, nil
	}
}

func decisionJSONObject(d *json.Decoder) (any, error) {
	object := map[string]any{}
	for d.More() {
		token, err := d.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return nil, fmt.Errorf("invalid decision input field")
		}
		if _, duplicate := object[name]; duplicate {
			return nil, fmt.Errorf("duplicate decision input field")
		}
		value, err := decisionJSONValue(d)
		if err != nil {
			return nil, err
		}
		object[name] = value
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return nil, fmt.Errorf("invalid decision input object")
	}
	return object, nil
}

func decisionJSONArray(d *json.Decoder) (any, error) {
	values := []any{}
	for d.More() {
		value, err := decisionJSONValue(d)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	end, err := d.Token()
	if err != nil || end != json.Delim(']') {
		return nil, fmt.Errorf("invalid decision input array")
	}
	return values, nil
}
