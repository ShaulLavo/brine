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
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return d, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return d, fmt.Errorf("invalid decision input document")
	}
	return d, nil
}

func CompareDecisionInputs(before, after []byte) (DecisionDrift, error) {
	a, err := decodeDecisionInput(before)
	if err != nil {
		return DecisionDrift{}, err
	}
	b, err := decodeDecisionInput(after)
	if err != nil {
		return DecisionDrift{}, err
	}
	out := DecisionDrift{Paths: []string{}}
	changed := func(path string) {
		out.Changed++
		if len(out.Paths) < MaxDecisionPaths && len(path) <= maxDecisionPathBytes {
			out.Paths = append(out.Paths, path)
		} else {
			out.Truncated = true
		}
	}
	compareDecisionValues(reflect.ValueOf(a), reflect.ValueOf(b), "", changed)
	slices.Sort(out.Paths)
	return out, nil
}

func compareDecisionValues(a, b reflect.Value, path string, changed func(string)) {
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
	typ := reflect.TypeFor[decisionDocument]()
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
