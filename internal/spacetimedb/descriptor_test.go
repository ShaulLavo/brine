package spacetimedb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
)

type descriptorPin struct {
	Variant          string `json:"variant"`
	WasmSHA256       string `json:"wasm_sha256"`
	SchemaSHA256     string `json:"schema_sha256"`
	DescriptorSHA256 string `json:"descriptor_sha256"`
}

func descriptorPins(t *testing.T) []descriptorPin {
	t.Helper()
	var record struct {
		Inputs []descriptorPin `json:"inputs"`
	}
	if err := json.Unmarshal(actualSchema(t, "descriptor-pins.json"), &record); err != nil {
		t.Fatal(err)
	}
	return record.Inputs
}

func descriptorSchema(t *testing.T, variant string) CanonicalSchema {
	t.Helper()
	value, err := DecodeDescriptorV10(actualSchema(t, "descriptor-"+variant+".json"), DefaultSchemaLimits())
	if err != nil {
		t.Fatal(variant, err)
	}
	return value
}

func TestFinalNativeDescriptorsUseSharedCanonicalizer(t *testing.T) {
	for _, pin := range descriptorPins(t) {
		raw := actualSchema(t, "descriptor-"+pin.Variant+".json")
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) != pin.DescriptorSHA256 {
			t.Fatal("actual captured descriptor bytes changed", pin.Variant)
		}
		value := descriptorSchema(t, pin.Variant)
		if value.SHA256() != pin.SchemaSHA256 || value.Version() != CanonicalizerVersion {
			t.Fatal("actual final descriptor semantics changed", pin.Variant, value.SHA256())
		}
	}
	if !bytes.Equal(descriptorSchema(t, "v1").Bytes(), mustNativeSchema(t).Bytes()) {
		t.Fatal("descriptor differs from qualified native API/restart schema")
	}
}

func mustNativeSchema(t *testing.T) CanonicalSchema {
	t.Helper()
	value, err := CanonicalSchemaV10(actualSchema(t, "schema-before.json"), DefaultSchemaLimits())
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDescriptorRefusesForeignEnvelopesAndLimits(t *testing.T) {
	for _, raw := range []string{
		`{"V9":{"sections":[]}}`, `{"V10":null}`, `{"sections":[]}`,
		`{"V10":{"sections":[]},"extra":true}`,
		`{"V10":{},"V10":{}}`, `{"V10":{}} {}`,
		`{"V10":{"sections":[{"Typespace":{"types":[],"types":[]}}]}}`,
		`{"V10":{"sections":[{"Procedures":[]}]}}`,
	} {
		if _, err := DecodeDescriptorV10([]byte(raw), DefaultSchemaLimits()); !errors.Is(err, ErrSchema) {
			t.Fatal("foreign descriptor accepted", raw, err)
		}
	}
	raw := actualSchema(t, "descriptor-v1.json")
	limits := DefaultSchemaLimits()
	limits.MaxBytes = len(raw) - 1
	if _, err := DecodeDescriptorV10(raw, limits); !errors.Is(err, ErrSchema) {
		t.Fatal("envelope byte limit ignored", err)
	}
	limits = DefaultSchemaLimits()
	limits.MaxDepth = 2
	if _, err := DecodeDescriptorV10(raw, limits); !errors.Is(err, ErrSchema) {
		t.Fatal("descriptor depth ignored", err)
	}
}

func TestDescriptorOwnsInputAndOutput(t *testing.T) {
	raw := actualSchema(t, "descriptor-default-column.json")
	value, err := DecodeDescriptorV10(raw, DefaultSchemaLimits())
	if err != nil {
		t.Fatal(err)
	}
	expected := value.SHA256()
	raw[0] = 'x'
	document := value.Bytes()
	document[0] = 'x'
	if value.SHA256() != expected || value.Bytes()[0] != '{' {
		t.Fatal("mutable descriptor evidence changed retained schema")
	}
}
