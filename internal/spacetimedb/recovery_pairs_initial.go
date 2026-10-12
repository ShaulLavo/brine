package spacetimedb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/nativeevidence"
)

const RecoveryInitialFormat = "brine-recovery-pairs-initial-v1"

type recoveryInitialRecord struct {
	Format        string                 `json:"format"`
	Contract      string                 `json:"contract"`
	Database      string                 `json:"database_identity"`
	Publisher     string                 `json:"publisher_identity"`
	NativeSHA256  string                 `json:"native_evidence_sha256"`
	PairsSHA256   string                 `json:"pairs_sha256"`
	Program       string                 `json:"program_wire_hash"`
	Wasm          string                 `json:"artifact_wasm_sha256"`
	Canonicalizer string                 `json:"canonicalizer"`
	Schema        string                 `json:"schema_sha256"`
	Requirement   SchemaRequirement      `json:"requirement"`
	Tables        []nativeevidence.Table `json:"initialized_user_tables"`
}

// RecoveryInitialProof establishes a first-published candidate's fixed data.
// It consumes no fabricated source and grants no guest or lifecycle review.
type RecoveryInitialProof struct {
	raw  []byte
	hash string
}

func (p RecoveryInitialProof) Bytes() []byte  { return bytes.Clone(p.raw) }
func (p RecoveryInitialProof) SHA256() string { return p.hash }
func (p RecoveryInitialProof) Requirements() []SchemaRequirement {
	if p.hash == "" {
		return nil
	}
	return []SchemaRequirement{VerifyNewTableData}
}

func CheckRecoveryPairsInitial(staged RecoveredPairs) (RecoveryInitialProof, error) {
	if staged.hash == "" || len(staged.value.Rows) != 0 {
		return RecoveryInitialProof{}, ErrRecoveryPairs
	}
	tables, err := initialRecoveryTables(staged)
	if err != nil {
		return RecoveryInitialProof{}, err
	}
	value := recoveryInitialRecord{
		Format: RecoveryInitialFormat, Contract: RecoveryPairsContract, Database: staged.value.Database, Publisher: staged.value.Publisher,
		NativeSHA256: staged.value.NativeSHA256, PairsSHA256: staged.hash, Program: staged.value.Program, Wasm: staged.value.Wasm,
		Canonicalizer: staged.value.Canonicalizer, Schema: staged.value.Schema, Requirement: VerifyNewTableData, Tables: tables,
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > MaxRecoveryPairsBytes {
		return RecoveryInitialProof{}, ErrRecoveryPairs
	}
	sum := sha256.Sum256(raw)
	return RecoveryInitialProof{raw: raw, hash: hex.EncodeToString(sum[:])}, nil
}

func initialRecoveryTables(staged RecoveredPairs) ([]nativeevidence.Table, error) {
	var initial []nativeevidence.Table
	for _, table := range staged.native.Tables() {
		if table.Type != "user" {
			continue
		}
		if table.Rows != initialRecoveryRowCount(table.Name) {
			return nil, ErrRecoveryPairs
		}
		initial = append(initial, table)
	}
	return initial, nil
}

func initialRecoveryRowCount(name string) uint64 {
	if name == "schema_marker" || name == "publisher" || name == "sequence_state" {
		return 1
	}
	return 0
}
