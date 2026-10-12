package datapublish

import "github.com/ShaulLavo/brine/internal/spacetimedb"

// Candidate retains expected content. Protected extraction and staged observation
// must establish its artifact/schema binding before any later effect admission.
type Candidate struct {
	wasm    string
	program string
	schema  spacetimedb.CanonicalSchema
}

func NewCandidate(wasmSHA256, programWireHash string, schema spacetimedb.CanonicalSchema) (Candidate, error) {
	if !digest(wasmSHA256) || !digest(programWireHash) || schema.Version() != spacetimedb.CanonicalizerVersion {
		return Candidate{}, ErrRecord
	}
	return Candidate{wasm: wasmSHA256, program: programWireHash, schema: schema}, nil
}
func (c Candidate) WasmSHA256() string                  { return c.wasm }
func (c Candidate) ProgramWireHash() string             { return c.program }
func (c Candidate) Schema() spacetimedb.CanonicalSchema { return c.schema }

type ReviewReferences struct {
	GuestCodeSHA256  string   `json:"guest_code_sha256"`
	RecoveryReleases []string `json:"recovery_releases"`
}

type NativePreflight struct {
	ResponseSHA256  string `json:"response_sha256"`
	TokenSHA256     string `json:"token_sha256"`
	PrivateTokenRef string `json:"private_token_ref"`
}

func (p NativePreflight) valid() bool {
	return digest(p.ResponseSHA256) && digest(p.TokenSHA256) && identity(p.PrivateTokenRef)
}
