package datapublish

import (
	"slices"

	"github.com/ShaulLavo/brine/internal/spec"
)

const ModuleArtifactLimit = 1 << 20

// Artifact owns reviewed bytes and their identities. It is not extraction or
// guest-review authority; those come from protected host evidence.
type Artifact struct {
	bytes   []byte
	wasm    spec.WasmSHA256
	program spec.RuntimeModuleHash
}

func NewArtifact(raw []byte) (Artifact, error) {
	if len(raw) > ModuleArtifactLimit {
		return Artifact{}, ErrRecord
	}
	owned := slices.Clone(raw)
	wasm, program, err := spec.ModuleIdentities(owned)
	if err != nil {
		return Artifact{}, ErrRecord
	}
	return Artifact{bytes: owned, wasm: wasm, program: program}, nil
}

func (a Artifact) Bytes() []byte                           { return slices.Clone(a.bytes) }
func (a Artifact) WasmSHA256() spec.WasmSHA256             { return a.wasm }
func (a Artifact) ProgramWireHash() spec.RuntimeModuleHash { return a.program }
func (Artifact) String() string                            { return "[private reviewed Wasm]" }
func (Artifact) GoString() string                          { return "[private reviewed Wasm]" }
func (Artifact) MarshalJSON() ([]byte, error)              { return nil, ErrRecord }
