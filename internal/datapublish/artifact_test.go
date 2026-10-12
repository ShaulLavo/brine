package datapublish

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
)

func TestPublicationArtifactOwnsActualNativeWitnessBytes(t *testing.T) {
	raw, err := os.ReadFile("../spec/testdata/managed-module.wasm")
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.Clone(raw)
	artifact, err := NewArtifact(raw)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.WasmSHA256() != "94369514b1160afc9d88df5dfe89fabe8f95a9aec57904931b1cd415ca96c2c6" || artifact.ProgramWireHash() != "594a40f8dc12577e4d185ad74d8a61d3894015a3f356a2f727214ef40024f072" {
		t.Fatal("actual artifact/API wire identities changed")
	}
	raw[0] = 'x'
	returned := artifact.Bytes()
	returned[0] = 'x'
	if !bytes.Equal(artifact.Bytes(), expected) {
		t.Fatal("caller changed retained reviewed artifact")
	}
	if fmt.Sprintf("%s %#v", artifact, artifact) != "[private reviewed Wasm] [private reviewed Wasm]" {
		t.Fatal("formatting exposed private reviewed bytes")
	}
	if _, err := json.Marshal(artifact); err == nil {
		t.Fatal("private reviewed bytes acquired a public JSON representation")
	}
}

func TestPublicationArtifactRefusesUnsupportedAndOversizedInput(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("not Wasm"), {0, 'a', 's', 'm', 2, 0, 0, 0}, make([]byte, ModuleArtifactLimit+1)} {
		if _, err := NewArtifact(raw); !errors.Is(err, ErrRecord) {
			t.Fatal("unsupported review transport accepted", err)
		}
	}
}
