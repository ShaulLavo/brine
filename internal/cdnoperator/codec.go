package cdnoperator

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type tagged struct {
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value"`
}

func encodeVariant(kind string, value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(tagged{Kind: kind, Value: raw})
}

func decodeVariant(raw []byte) (tagged, error) {
	fields, err := strictjson.Object(raw, "kind", "value")
	if err != nil {
		return tagged{}, cdn.ErrInvalid
	}
	kind, err := strictjson.Value[string](fields["kind"])
	if err != nil {
		return tagged{}, cdn.ErrInvalid
	}
	return tagged{Kind: kind, Value: fields["value"]}, nil
}

// DecodeExact checks field spelling, duplicate keys and every nested object.
func DecodeExact(raw []byte, target any) error {
	typeOf := reflect.TypeOf(target)
	if typeOf == nil || typeOf.Kind() != reflect.Pointer || checkShape(raw, typeOf.Elem(), 0) != nil {
		return cdn.ErrInvalid
	}
	if json.Unmarshal(raw, target) != nil {
		return cdn.ErrInvalid
	}
	return nil
}

var timeType = reflect.TypeFor[time.Time]()
var rawType = reflect.TypeFor[json.RawMessage]()
var preconditionType = reflect.TypeFor[cdn.PreconditionSource]()

func checkShape(raw []byte, typ reflect.Type, depth int) error {
	if depth > 32 || !json.Valid(raw) {
		return cdn.ErrInvalid
	}
	if typ == timeType || typ == rawType {
		return nil
	}
	if typ == preconditionType {
		return checkPrecondition(raw, depth)
	}
	if typ.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil
		}
		return checkShape(raw, typ.Elem(), depth+1)
	}
	switch typ.Kind() {
	case reflect.Struct:
		return checkStruct(raw, typ, depth)
	case reflect.Slice, reflect.Array:
		return checkArray(raw, typ.Elem(), depth)
	case reflect.Interface:
		return nil
	default:
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return cdn.ErrInvalid
		}
		return nil
	}
}

func checkStruct(raw []byte, typ reflect.Type, depth int) error {
	names := make([]string, 0, typ.NumField())
	types := make(map[string]reflect.Type, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if !field.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		names = append(names, name)
		types[name] = field.Type
	}
	fields, err := strictjson.Object(raw, names...)
	if err != nil {
		return cdn.ErrInvalid
	}
	for name, field := range fields {
		if checkShape(field, types[name], depth+1) != nil {
			return cdn.ErrInvalid
		}
	}
	return nil
}

func checkArray(raw []byte, typ reflect.Type, depth int) error {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil || len(values) > 1024 {
		return cdn.ErrInvalid
	}
	for _, value := range values {
		if checkShape(value, typ, depth+1) != nil {
			return cdn.ErrInvalid
		}
	}
	return nil
}

func checkPrecondition(raw []byte, depth int) error {
	if fields, err := strictjson.Object(raw, "kind", "before", "resource"); err == nil {
		kind, _ := strictjson.Value[string](fields["kind"])
		if kind == "initial" && checkShape(fields["before"], reflect.TypeFor[cdn.Fingerprint](), depth+1) == nil {
			return checkShape(fields["resource"], reflect.TypeFor[cdn.OwnedResource](), depth+1)
		}
	}
	fields, err := strictjson.Object(raw, "kind", "step_index")
	if err != nil {
		return cdn.ErrInvalid
	}
	kind, _ := strictjson.Value[string](fields["kind"])
	_, err = strictjson.Value[uint32](fields["step_index"])
	if kind != "prior_owned_receipt" || err != nil {
		return cdn.ErrInvalid
	}
	return nil
}

func encodeWork(work cdn.Work) (json.RawMessage, error) {
	switch value := work.(type) {
	case cdn.NeedEffect:
		return encodeVariant("need_effect", value)
	case cdn.NeedObservation:
		request, err := encodeObservationRequest(value.Request)
		if err != nil {
			return nil, err
		}
		return encodeVariant("need_observation", struct {
			Request json.RawMessage `json:"request"`
		}{request})
	case cdn.Waiting:
		return encodeVariant("waiting", value)
	case cdn.Finished:
		return encodeVariant("finished", value)
	default:
		return nil, cdn.ErrInvalid
	}
}

func decodeWork(raw []byte) (cdn.Work, error) {
	variant, err := decodeVariant(raw)
	if err != nil {
		return nil, err
	}
	switch variant.Kind {
	case "need_effect":
		var value cdn.NeedEffect
		err = DecodeExact(variant.Value, &value)
		return value, err
	case "need_observation":
		fields, err := strictjson.Object(variant.Value, "request")
		if err != nil {
			return nil, cdn.ErrInvalid
		}
		request, err := decodeObservationRequest(fields["request"])
		return cdn.NeedObservation{Request: request}, err
	case "waiting":
		var value cdn.Waiting
		err = DecodeExact(variant.Value, &value)
		return value, err
	case "finished":
		var value cdn.Finished
		err = DecodeExact(variant.Value, &value)
		return value, err
	default:
		return nil, cdn.ErrInvalid
	}
}

func encodeObservationRequest(request cdn.ObservationRequest) (json.RawMessage, error) {
	switch value := request.(type) {
	case cdn.InspectIssued:
		return encodeVariant("inspect_issued", value)
	case cdn.PrepareStep:
		return encodeVariant("prepare_step", value)
	case cdn.NeedQualification:
		return encodeVariant("qualify", value)
	default:
		return nil, cdn.ErrInvalid
	}
}

func decodeObservationRequest(raw []byte) (cdn.ObservationRequest, error) {
	variant, err := decodeVariant(raw)
	if err != nil {
		return nil, err
	}
	switch variant.Kind {
	case "inspect_issued":
		var value cdn.InspectIssued
		err = DecodeExact(variant.Value, &value)
		return value, err
	case "prepare_step":
		var value cdn.PrepareStep
		err = DecodeExact(variant.Value, &value)
		return value, err
	case "qualify":
		var value cdn.NeedQualification
		err = DecodeExact(variant.Value, &value)
		return value, err
	default:
		return nil, cdn.ErrInvalid
	}
}

func (p Progress) MarshalJSON() ([]byte, error) {
	work, err := encodeWork(p.Work)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Operation cdn.OperationID `json:"operation"`
		Plan      cdn.Plan        `json:"plan"`
		Work      json.RawMessage `json:"work"`
	}{p.Operation, p.Plan, work})
}

func (p *Progress) UnmarshalJSON(raw []byte) error {
	fields, err := strictjson.Object(raw, "operation", "plan", "work")
	if err != nil || DecodeExact(fields["plan"], &p.Plan) != nil || json.Unmarshal(fields["operation"], &p.Operation) != nil {
		return cdn.ErrInvalid
	}
	p.Work, err = decodeWork(fields["work"])
	if err != nil {
		return err
	}
	return p.Validate()
}

func (r ResultRequest) MarshalJSON() ([]byte, error) {
	var raw json.RawMessage
	var err error
	switch value := r.Result.(type) {
	case cdn.Acknowledged:
		raw, err = encodeVariant("acknowledged", value)
	case cdn.Refused:
		raw, err = encodeVariant("refused", value)
	case cdn.Uncertain:
		raw, err = encodeVariant("uncertain", value)
	default:
		return nil, cdn.ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Operation cdn.OperationID `json:"operation"`
		Result    json.RawMessage `json:"result"`
	}{r.Operation, raw})
}

func (r *ResultRequest) UnmarshalJSON(raw []byte) error {
	fields, err := strictjson.Object(raw, "operation", "result")
	if err != nil || json.Unmarshal(fields["operation"], &r.Operation) != nil || !ValidOperation(r.Operation) {
		return cdn.ErrInvalid
	}
	variant, err := decodeVariant(fields["result"])
	if err != nil {
		return err
	}
	switch variant.Kind {
	case "acknowledged":
		var value cdn.Acknowledged
		err = DecodeExact(variant.Value, &value)
		r.Result = value
	case "refused":
		var value cdn.Refused
		err = DecodeExact(variant.Value, &value)
		r.Result = value
	case "uncertain":
		var value cdn.Uncertain
		err = DecodeExact(variant.Value, &value)
		r.Result = value
	default:
		return cdn.ErrInvalid
	}
	return err
}

func (r InspectionRequest) MarshalJSON() ([]byte, error) {
	var raw json.RawMessage
	var err error
	switch value := r.Inspection.(type) {
	case cdn.Observed:
		raw, err = encodeVariant("observed", value)
	case cdn.Completed:
		raw, err = encodeVariant("completed", value)
	default:
		return nil, cdn.ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Operation  cdn.OperationID `json:"operation"`
		Inspection json.RawMessage `json:"inspection"`
	}{r.Operation, raw})
}

func (r *InspectionRequest) UnmarshalJSON(raw []byte) error {
	fields, err := strictjson.Object(raw, "operation", "inspection")
	if err != nil || json.Unmarshal(fields["operation"], &r.Operation) != nil || !ValidOperation(r.Operation) {
		return cdn.ErrInvalid
	}
	variant, err := decodeVariant(fields["inspection"])
	if err != nil {
		return err
	}
	switch variant.Kind {
	case "observed":
		var value cdn.Observed
		err = DecodeExact(variant.Value, &value)
		r.Inspection = value
	case "completed":
		var value cdn.Completed
		err = DecodeExact(variant.Value, &value)
		r.Inspection = value
	default:
		return cdn.ErrInvalid
	}
	return err
}
