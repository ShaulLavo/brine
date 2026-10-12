package spacetimedb

import "github.com/ShaulLavo/brine/internal/strictjson"

// DecodeDescriptorV10 parses expected metadata, not an artifact or operation proof.
func DecodeDescriptorV10(raw []byte, limits SchemaLimits) (CanonicalSchema, error) {
	if !limits.valid() || len(raw) == 0 || len(raw) > limits.MaxBytes {
		return CanonicalSchema{}, ErrSchema
	}
	fields, err := strictjson.Object(raw, "V10")
	if err != nil {
		return CanonicalSchema{}, ErrSchema
	}
	return CanonicalSchemaV10(fields["V10"], limits)
}
