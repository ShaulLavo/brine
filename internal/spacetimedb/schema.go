package spacetimedb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

const CanonicalizerVersion = "spacetimedb-v10-expanded-v2"

var ErrSchema = errors.New("unsupported or invalid SpacetimeDB v10 schema")

type SchemaLimits struct {
	MaxBytes        int
	MaxTypes        int
	MaxNodes        int
	MaxDepth        int
	MaxDeclarations int
}

func DefaultSchemaLimits() SchemaLimits {
	return SchemaLimits{MaxBytes: 1 << 20, MaxTypes: 1024, MaxNodes: 100_000, MaxDepth: 64, MaxDeclarations: 4096}
}

func (l SchemaLimits) valid() bool {
	max := DefaultSchemaLimits()
	return l.MaxBytes > 0 && l.MaxBytes <= max.MaxBytes && l.MaxTypes > 0 && l.MaxTypes <= max.MaxTypes &&
		l.MaxNodes > 0 && l.MaxNodes <= max.MaxNodes && l.MaxDepth > 0 && l.MaxDepth <= max.MaxDepth &&
		l.MaxDeclarations > 0 && l.MaxDeclarations <= max.MaxDeclarations
}

type CanonicalSchema struct {
	document []byte
	hash     string
}

func (s CanonicalSchema) Version() string {
	if s.hash == "" {
		return ""
	}
	return CanonicalizerVersion
}
func (s CanonicalSchema) SHA256() string { return s.hash }
func (s CanonicalSchema) Bytes() []byte  { return bytes.Clone(s.document) }
func (s CanonicalSchema) Marker() string {
	if s.hash == "" {
		return ""
	}
	return CanonicalizerVersion + "." + s.hash
}

func CanonicalSchemaV10(raw []byte, limits SchemaLimits) (CanonicalSchema, error) {
	if !limits.valid() || len(raw) == 0 || len(raw) > limits.MaxBytes {
		return CanonicalSchema{}, ErrSchema
	}
	value, err := decodeDocument(raw, limits)
	if err != nil {
		return CanonicalSchema{}, ErrSchema
	}
	sections, err := decodeSections(value)
	if err != nil {
		return CanonicalSchema{}, ErrSchema
	}
	normalized, err := normalizeSections(sections, limits)
	if err != nil {
		return CanonicalSchema{}, ErrSchema
	}
	document, err := encodeDocument(map[string]any{"canonicalizer": CanonicalizerVersion, "sections": normalized}, limits.MaxBytes)
	if err != nil {
		return CanonicalSchema{}, ErrSchema
	}
	sum := sha256.Sum256(document)
	return CanonicalSchema{document: document, hash: hex.EncodeToString(sum[:])}, nil
}
