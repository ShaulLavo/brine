package spec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"golang.org/x/crypto/sha3"
)

const MaxWasmBytes = 64 << 20

func moduleIdentities(wasm []byte) (WasmSHA256, RuntimeModuleHash, error) {
	if len(wasm) < 8 || len(wasm) > MaxWasmBytes || !bytes.Equal(wasm[:8], []byte{0, 'a', 's', 'm', 1, 0, 0, 0}) {
		return "", "", data.ErrInvalid
	}
	artifact := sha256.Sum256(wasm)
	native := sha3.NewLegacyKeccak256()
	_, _ = native.Write(wasm)
	wire := native.Sum(nil)
	// Native JSON serializes Hash as a little-endian u256, unlike Hash::Display.
	slices.Reverse(wire)
	return WasmSHA256(hex.EncodeToString(artifact[:])), RuntimeModuleHash(hex.EncodeToString(wire)), nil
}
