package spacetimedb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"

	"golang.org/x/crypto/sha3"
)

const MaxWasmBytes = 64 << 20

type WasmSHA256 string

var ErrModuleIdentity = errors.New("invalid SpacetimeDB Wasm artifact")

func ModuleIdentities(wasm []byte) (WasmSHA256, RuntimeModuleHash, error) {
	if len(wasm) < 8 || len(wasm) > MaxWasmBytes || !bytes.Equal(wasm[:8], []byte{0, 'a', 's', 'm', 1, 0, 0, 0}) {
		return "", "", ErrModuleIdentity
	}
	artifact := sha256.Sum256(wasm)
	native := sha3.NewLegacyKeccak256()
	_, _ = native.Write(wasm)
	wire := native.Sum(nil)
	slices.Reverse(wire)
	return WasmSHA256(hex.EncodeToString(artifact[:])), RuntimeModuleHash(hex.EncodeToString(wire)), nil
}
