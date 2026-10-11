package spacetimedb

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type jsonReader struct {
	decoder *json.Decoder
	limits  SchemaLimits
	nodes   int
}

func decodeDocument(raw []byte, limits SchemaLimits) (any, error) {
	if !utf8.Valid(raw) || !validUnicodeEscapes(raw) {
		return nil, ErrSchema
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	r := jsonReader{decoder: d, limits: limits}
	value, err := r.value(0)
	if err != nil {
		return nil, ErrSchema
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrSchema
	}
	return value, nil
}

func (r *jsonReader) value(depth int) (any, error) {
	r.nodes++
	if depth > r.limits.MaxDepth || r.nodes > r.limits.MaxNodes {
		return nil, ErrSchema
	}
	token, err := r.decoder.Token()
	if err != nil {
		return nil, ErrSchema
	}
	switch token {
	case json.Delim('{'):
		return r.object(depth + 1)
	case json.Delim('['):
		return r.array(depth + 1)
	}
	if _, delimiter := token.(json.Delim); delimiter {
		return nil, ErrSchema
	}
	if number, ok := token.(json.Number); ok {
		if len(number.String()) > 79 || strings.ContainsAny(number.String(), ".eE") {
			return nil, ErrSchema
		}
		integer, valid := new(big.Int).SetString(number.String(), 10)
		if !valid {
			return nil, ErrSchema
		}
		return json.Number(integer.String()), nil
	}
	return token, nil
}

func validUnicodeEscapes(raw []byte) bool {
	for index := 0; index < len(raw); index++ {
		if raw[index] != '\\' {
			continue
		}
		if index+1 >= len(raw) {
			return false
		}
		if raw[index+1] != 'u' {
			index++
			continue
		}
		unit, ok := unicodeEscape(raw[index:])
		if !ok || unit >= 0xdc00 && unit <= 0xdfff {
			return false
		}
		if unit >= 0xd800 && unit <= 0xdbff {
			ok = validUnicodePair(raw[index+6:])
			index += 6
		}
		if !ok {
			return false
		}
		index += 5
	}
	return true
}

func validUnicodePair(raw []byte) bool {
	pair, ok := unicodeEscape(raw)
	return ok && pair >= 0xdc00 && pair <= 0xdfff
}

func unicodeEscape(raw []byte) (uint64, bool) {
	if len(raw) < 6 || raw[0] != '\\' || raw[1] != 'u' {
		return 0, false
	}
	value, err := strconv.ParseUint(string(raw[2:6]), 16, 16)
	return value, err == nil
}

func (r *jsonReader) object(depth int) (any, error) {
	object := map[string]any{}
	for r.decoder.More() {
		token, err := r.decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, ErrSchema
		}
		if _, duplicate := object[key]; duplicate {
			return nil, ErrSchema
		}
		value, err := r.value(depth)
		if err != nil {
			return nil, ErrSchema
		}
		object[key] = value
	}
	end, err := r.decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, ErrSchema
	}
	return object, nil
}

func (r *jsonReader) array(depth int) (any, error) {
	array := []any{}
	for r.decoder.More() {
		value, err := r.value(depth)
		if err != nil {
			return nil, ErrSchema
		}
		array = append(array, value)
	}
	end, err := r.decoder.Token()
	if err != nil || end != json.Delim(']') {
		return nil, ErrSchema
	}
	return array, nil
}

type jsonWriter struct {
	buffer []byte
	limit  int
}

func encodeDocument(value any, limit int) ([]byte, error) {
	w := jsonWriter{limit: limit}
	if err := w.value(value); err != nil {
		return nil, err
	}
	return w.buffer, nil
}

func (w *jsonWriter) value(value any) error {
	if len(w.buffer) > w.limit {
		return ErrSchema
	}
	var err error
	switch value := value.(type) {
	case map[string]any:
		err = w.object(value)
	case []any:
		err = w.array(value)
	case string:
		err = w.string(value)
	case json.Number:
		w.buffer = append(w.buffer, value.String()...)
	case bool:
		w.buffer = strconv.AppendBool(w.buffer, value)
	case nil:
		w.buffer = append(w.buffer, "null"...)
	default:
		return ErrSchema
	}
	if err != nil || len(w.buffer) > w.limit {
		return ErrSchema
	}
	return nil
}

func (w *jsonWriter) object(value map[string]any) error {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	w.buffer = append(w.buffer, '{')
	for index, key := range keys {
		if index != 0 {
			w.buffer = append(w.buffer, ',')
		}
		if err := w.string(key); err != nil {
			return err
		}
		w.buffer = append(w.buffer, ':')
		if err := w.value(value[key]); err != nil {
			return err
		}
	}
	w.buffer = append(w.buffer, '}')
	return nil
}

func (w *jsonWriter) array(value []any) error {
	w.buffer = append(w.buffer, '[')
	for index, item := range value {
		if index != 0 {
			w.buffer = append(w.buffer, ',')
		}
		if err := w.value(item); err != nil {
			return err
		}
	}
	w.buffer = append(w.buffer, ']')
	return nil
}

func (w *jsonWriter) string(value string) error {
	if len(value) > w.limit-len(w.buffer) {
		return ErrSchema
	}
	w.buffer = append(w.buffer, '"')
	for _, character := range value {
		w.character(character)
		if len(w.buffer) > w.limit {
			return ErrSchema
		}
	}
	w.buffer = append(w.buffer, '"')
	return nil
}

func (w *jsonWriter) character(value rune) {
	switch value {
	case '"', '\\':
		w.buffer = append(w.buffer, '\\', byte(value))
	case '\b':
		w.buffer = append(w.buffer, '\\', 'b')
	case '\f':
		w.buffer = append(w.buffer, '\\', 'f')
	case '\n':
		w.buffer = append(w.buffer, '\\', 'n')
	case '\r':
		w.buffer = append(w.buffer, '\\', 'r')
	case '\t':
		w.buffer = append(w.buffer, '\\', 't')
	default:
		if value < 32 {
			const hex = "0123456789abcdef"
			w.buffer = append(w.buffer, '\\', 'u', '0', '0', hex[value>>4], hex[value&15])
			return
		}
		w.buffer = utf8.AppendRune(w.buffer, value)
	}
}
