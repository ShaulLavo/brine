package plan

import (
	"encoding/json"
	"fmt"
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
		compareDecisionFields(a, b, path, changed)
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

func compareDecisionFields(a, b reflect.Value, path string, changed func(string)) {
	for i := 0; i < a.NumField(); i++ {
		field := a.Type().Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if !field.IsExported() || name == "" || name == "-" {
			continue
		}
		next := name
		if path != "" {
			next = path + "." + name
		}
		compareDecisionValues(a.Field(i), b.Field(i), next, changed)
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
		name, indices, indexed := strings.Cut(segment, "[")
		typ = decisionPathField(typ, name)
		if typ == nil {
			return false
		}
		if indexed {
			typ = decisionPathIndices(typ, indices)
		}
		if typ == nil {
			return false
		}
	}
	return true
}

func decisionIndirect(typ reflect.Type) reflect.Type {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return typ
}

func decisionPathField(typ reflect.Type, name string) reflect.Type {
	typ = decisionIndirect(typ)
	if typ.Kind() != reflect.Struct || typ == reflect.TypeFor[time.Time]() {
		return nil
	}
	for field := range typ.Fields() {
		if field.IsExported() && strings.Split(field.Tag.Get("json"), ",")[0] == name {
			return field.Type
		}
	}
	return nil
}

func decisionPathIndices(typ reflect.Type, indices string) reflect.Type {
	for {
		typ = decisionIndirect(typ)
		index, rest, ok := strings.Cut(indices, "]")
		n, err := strconv.Atoi(index)
		if !ok || err != nil || n < 0 || strconv.Itoa(n) != index || typ.Kind() != reflect.Slice {
			return nil
		}
		typ = typ.Elem()
		if rest == "" {
			return typ
		}
		indices, ok = strings.CutPrefix(rest, "[")
		if !ok {
			return nil
		}
	}
}
