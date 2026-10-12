package spacetimedb

import (
	"errors"
	"os"
	"testing"
)

func TestSharedModuleIdentitiesMatchNativeWireWitness(t *testing.T) {
	wasm, err := os.ReadFile("../spec/testdata/managed-module.wasm")
	if err != nil {
		t.Fatal(err)
	}
	artifact, wire, err := ModuleIdentities(wasm)
	if err != nil {
		t.Fatal(err)
	}
	if artifact != "94369514b1160afc9d88df5dfe89fabe8f95a9aec57904931b1cd415ca96c2c6" || wire != "594a40f8dc12577e4d185ad74d8a61d3894015a3f356a2f727214ef40024f072" {
		t.Fatal("artifact/native API wire identity differs from executed v2.11.0 witness")
	}
}

func TestSharedModuleIdentitiesRefuseOtherArtifacts(t *testing.T) {
	for _, wasm := range [][]byte{nil, []byte("not Wasm"), {0, 'a', 's', 'm', 2, 0, 0, 0}, make([]byte, MaxWasmBytes+1)} {
		if _, _, err := ModuleIdentities(wasm); !errors.Is(err, ErrModuleIdentity) {
			t.Fatal("unsupported Wasm artifact admitted")
		}
	}
}
