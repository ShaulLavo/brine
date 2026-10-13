package nativeevidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestStoredEvidenceKeepsExactBoundedRequestLimits(t *testing.T) {
	value := validRecord()
	requested := MaximumLimits()
	requested.StoredBytes, requested.FileBytes = 64<<20, 32<<20
	requested.ExpandedBytes, requested.ExpandedFileBytes = 128<<20, 64<<20
	requested.StateBytes = 64 << 20
	value.Limits = requested
	original, err := Decode(encoded(t, value), requested)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Decode(original.Bytes(), MaximumLimits()); !errors.Is(err, ErrEvidence) {
		t.Fatal("live output lost exact requested-limit binding", err)
	}
	retained, err := DecodeStored(original.Bytes())
	if err != nil || retained.RequestedLimits() != requested || retained.SHA256() != original.SHA256() || !bytes.Equal(retained.Bytes(), original.Bytes()) || !retained.SamePersistedState(original) {
		t.Fatal("stored evidence changed limits/canonical bytes/hash/state", err)
	}
	opaque, err := Decode(original.Bytes(), original.RequestedLimits())
	if err != nil || opaque.SHA256() != original.SHA256() {
		t.Fatal("opaque evidence reread lost exact limit contract", err)
	}
	max, err := DecodeStored(encoded(t, validRecord()))
	if err != nil || max.RequestedLimits() != MaximumLimits() {
		t.Fatal("existing maximum-bound record changed", err)
	}
}

func TestStoredEvidenceRefusesBadLimitsAndSyntax(t *testing.T) {
	for _, mutate := range []func(*record){
		func(v *record) { v.Limits.StoredBytes = MaximumLimits().StoredBytes + 1 },
		func(v *record) { v.Limits.OutputBytes = MaximumLimits().OutputBytes + 1 },
		func(v *record) { v.Limits.MemoryBytes = 0 },
		func(v *record) { v.Limits.Files = 1 },
		func(v *record) { v.Limits.OutputBytes = 256 },
		func(v *record) { v.Limits.Tables = 1 },
		func(v *record) { v.Limits.Rows = 1 },
		func(v *record) { v.Input.StoredBytes = v.Limits.StoredBytes + 1 },
	} {
		value := validRecord()
		mutate(&value)
		if _, err := DecodeStored(encoded(t, value)); !errors.Is(err, ErrEvidence) {
			t.Fatal("invalid retained limit/usage admitted", err)
		}
	}
	raw := string(encoded(t, validRecord()))
	for _, attack := range []string{
		strings.Replace(raw, `"limits":`, `"limits":null,"limits":`, 1),
		strings.Replace(raw, `"limits":`, `"Limits":`, 1),
		strings.Replace(raw, `"files":8192`, `"files":8192,"Files":8192`, 1),
		strings.Replace(raw, `"files":8192`, `"files":null`, 1),
		strings.Replace(raw, `"limits":{`, `"limits":{"unknown":1,`, 1),
		raw + ` {}`,
		strings.Replace(raw, `"outcome":"observed"`, `"outcome":null`, 1),
	} {
		if _, err := DecodeStored([]byte(attack)); !errors.Is(err, ErrEvidence) {
			t.Fatal("retained syntax attack admitted", err)
		}
	}
	if _, err := DecodeStored(make([]byte, MaximumLimits().OutputBytes+1)); !errors.Is(err, ErrEvidence) {
		t.Fatal("unbounded retained input admitted", err)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &object) != nil {
		t.Fatal("test record decode")
	}
	delete(object, "limits")
	missing, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeStored(missing); !errors.Is(err, ErrEvidence) {
		t.Fatal("missing recorded request limits admitted", err)
	}
}

func TestStoredEvidenceParsingDoesNotAuthenticateChangedContent(t *testing.T) {
	value := validRecord()
	original, err := DecodeStored(encoded(t, value))
	if err != nil {
		t.Fatal(err)
	}
	value.Owner = strings.Repeat("09", 32)
	changed, err := DecodeStored(encoded(t, value))
	if err != nil || changed.SHA256() == original.SHA256() || changed.SamePersistedState(original) || changed.OwnerIdentity() == original.OwnerIdentity() {
		t.Fatal("changed owner evidence retained original hash/state identity", err)
	}
}
