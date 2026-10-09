package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/ShaulLavo/brine/internal/plan"
)

func decodeBrineState(data []byte) (plan.BrineState, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := checkStateJSON(decoder, reflect.TypeFor[plan.BrineState]()); err != nil {
		return plan.BrineState{}, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return plan.BrineState{}, fmt.Errorf("expected one state object")
	}
	var state plan.BrineState
	if err := json.Unmarshal(data, &state); err != nil {
		return plan.BrineState{}, err
	}
	return state, nil
}

// Check exact keys before encoding/json can accept duplicates, aliases or
// missing hash input. Observation values are optional; Build checks their status.
func checkStateJSON(decoder *json.Decoder, t reflect.Type) error {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return fmt.Errorf("state fields must not be null")
	}
	switch t.Kind() {
	case reflect.Struct:
		if token != json.Delim('{') {
			return fmt.Errorf("state object required")
		}
		fields := map[string]reflect.Type{}
		required := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")
			fields[tag[0]] = field.Type
			if len(tag) == 1 || tag[1] != "omitempty" {
				required[tag[0]] = true
			}
		}
		seen := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok {
				return fmt.Errorf("state field required")
			}
			field, ok := fields[key]
			if !ok || seen[key] {
				return fmt.Errorf("unknown or repeated state field")
			}
			seen[key] = true
			if err := checkStateJSON(decoder, field); err != nil {
				return err
			}
		}
		for name := range required {
			if !seen[name] {
				return fmt.Errorf("missing state field")
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("state object terminator required")
		}
	case reflect.Slice:
		if token != json.Delim('[') {
			return fmt.Errorf("state array required")
		}
		for decoder.More() {
			if err := checkStateJSON(decoder, t.Elem()); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("state array terminator required")
		}
	default:
		if _, nested := token.(json.Delim); nested {
			return fmt.Errorf("state scalar required")
		}
	}
	return nil
}
