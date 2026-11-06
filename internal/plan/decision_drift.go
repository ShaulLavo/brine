package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/policy"
)

const MaxDecisionPaths = 24
const maxDecisionPathBytes = 160
const MaxDecisionInputBytes = 16 << 20

// DecisionDrift reports schema-owned paths only. Map keys and all values stay private.
type DecisionDrift struct {
	Paths     []string `json:"paths"`
	Changed   int      `json:"changed"`
	Truncated bool     `json:"truncated"`
}

type decisionDocument struct {
	Desired  policy.Desired `json:"desired"`
	Snapshot decisionFacts  `json:"snapshot"`
	State    BrineState     `json:"brine_state"`
	Plan     Plan           `json:"plan"`
}

type managedDecisionFacts struct {
	Snapshot decisionFacts `json:"snapshot"`
	Managed  ManagedFacts  `json:"managed"`
}

type managedDecisionDocument struct {
	Desired  policy.Desired       `json:"desired"`
	Snapshot managedDecisionFacts `json:"snapshot"`
	State    BrineState           `json:"brine_state"`
	Plan     Plan                 `json:"plan"`
}

func (p Plan) DecisionInput() []byte { return slices.Clone(p.decisionInput) }

func (p Plan) WithDecisionInput(raw []byte) (Plan, error) {
	if len(raw) > MaxDecisionInputBytes || hash(raw) != p.Hash {
		return Plan{}, fmt.Errorf("invalid decision input identity")
	}
	p.decisionInput = slices.Clone(raw)
	return p, nil
}

func decodeDecisionInput(raw []byte) (decisionDocument, error) {
	var d decisionDocument
	if len(raw) == 0 || len(raw) > MaxDecisionInputBytes {
		return d, fmt.Errorf("invalid decision input size")
	}
	err := decodeDecisionDocument(raw, &d)
	return d, err
}

func decodeDecisionDocument(raw []byte, document any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(document); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("invalid decision input document")
	}
	return nil
}

func decisionInputValue(raw []byte) (reflect.Value, error) {
	if len(raw) == 0 || len(raw) > MaxDecisionInputBytes {
		return reflect.Value{}, fmt.Errorf("invalid decision input size")
	}
	var shape struct {
		Snapshot struct {
			Managed json.RawMessage `json:"managed"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		return reflect.Value{}, err
	}
	if len(shape.Snapshot.Managed) != 0 {
		var d managedDecisionDocument
		err := decodeDecisionDocument(raw, &d)
		return reflect.ValueOf(d), err
	}
	d, err := decodeDecisionInput(raw)
	return reflect.ValueOf(d), err
}

func CompareDecisionInputs(before, after []byte) (DecisionDrift, error) {
	a, err := decisionInputValue(before)
	if err != nil {
		return DecisionDrift{}, err
	}
	b, err := decisionInputValue(after)
	if err != nil {
		return DecisionDrift{}, err
	}
	out := DecisionDrift{Paths: []string{}}
	for i := 0; i < a.NumField(); i++ {
		field := a.Type().Field(i)
		compareDecisionValues(a.Field(i), b.FieldByName(field.Name), field.Tag.Get("json"), out.addPath)
	}
	slices.Sort(out.Paths)
	return out, nil
}

// Invalid generated paths count as omitted changes, never poison valid diagnostics.
func (d *DecisionDrift) addPath(path string) {
	d.Changed++
	if len(d.Paths) < MaxDecisionPaths && len(path) <= maxDecisionPathBytes && validDecisionPath(path) {
		d.Paths = append(d.Paths, path)
	} else {
		d.Truncated = true
	}
}

func compareDecisionValues(a, b reflect.Value, path string, changed func(string)) {
	if a.Type() != b.Type() {
		changed(path)
		return
	}
	if reflect.DeepEqual(a.Interface(), b.Interface()) {
		return
	}
	switch a.Kind() {
	case reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			changed(path)
			return
		}
		compareDecisionValues(a.Elem(), b.Elem(), path, changed)
	case reflect.Struct:
		if a.Type() == reflect.TypeFor[time.Time]() {
			changed(path)
			return
		}
		for i := 0; i < a.NumField(); i++ {
			field := a.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			next := name
			if path != "" {
				next = path + "." + name
			}
			compareDecisionValues(a.Field(i), b.Field(i), next, changed)
		}
	case reflect.Slice:
		if a.IsNil() != b.IsNil() || a.Len() != b.Len() {
			changed(path)
			return
		}
		for i := 0; i < a.Len(); i++ {
			compareDecisionValues(a.Index(i), b.Index(i), path+"["+strconv.Itoa(i)+"]", changed)
		}
	default:
		changed(path)
	}
}

func (d DecisionDrift) Valid() bool {
	if d.Paths == nil || len(d.Paths) > MaxDecisionPaths || d.Changed < len(d.Paths) || d.Truncated != (d.Changed > len(d.Paths)) {
		return false
	}
	for i, path := range d.Paths {
		if len(path) == 0 || len(path) > maxDecisionPathBytes || i > 0 && d.Paths[i-1] >= path || !validDecisionPath(path) {
			return false
		}
	}
	return true
}

func validDecisionPath(path string) bool {
	return validDecisionPathFor(path, reflect.TypeFor[decisionDocument]()) || validDecisionPathFor(path, reflect.TypeFor[managedDecisionDocument]())
}

func validDecisionPathFor(path string, typ reflect.Type) bool {
	for _, segment := range strings.Split(path, ".") {
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || typ == reflect.TypeFor[time.Time]() {
			return false
		}
		name, indices, _ := strings.Cut(segment, "[")
		var found reflect.Type
		for field := range typ.Fields() {
			if field.IsExported() && strings.Split(field.Tag.Get("json"), ",")[0] == name {
				found = field.Type
				break
			}
		}
		if found == nil {
			return false
		}
		typ = found
		for indices != "" {
			for typ.Kind() == reflect.Pointer {
				typ = typ.Elem()
			}
			index, rest, ok := strings.Cut(indices, "]")
			n, err := strconv.Atoi(index)
			if !ok || err != nil || n < 0 || strconv.Itoa(n) != index || typ.Kind() != reflect.Slice {
				return false
			}
			typ = typ.Elem()
			if rest == "" {
				indices = ""
			} else {
				indices, ok = strings.CutPrefix(rest, "[")
				if !ok {
					return false
				}
			}
		}
		if strings.Contains(segment, "[") && !strings.HasSuffix(segment, "]") {
			return false
		}
	}
	return true
}
