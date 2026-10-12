package spacetimedb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/nativeevidence"
)

const RecoveryPreservationFormat = "brine-recovery-pairs-preservation-v1"

type recoveryPreservationRecord struct {
	Format        string                 `json:"format"`
	Contract      string                 `json:"contract"`
	Database      string                 `json:"database_identity"`
	Publisher     string                 `json:"publisher_identity"`
	SourceNative  string                 `json:"source_native_evidence_sha256"`
	StagedNative  string                 `json:"staged_native_evidence_sha256"`
	SourcePairs   string                 `json:"source_pairs_sha256"`
	StagedPairs   string                 `json:"staged_pairs_sha256"`
	SourceProgram string                 `json:"source_program_wire_hash"`
	StagedProgram string                 `json:"staged_program_wire_hash"`
	SourceWasm    string                 `json:"source_artifact_wasm_sha256"`
	StagedWasm    string                 `json:"staged_artifact_wasm_sha256"`
	SourceSchema  string                 `json:"source_schema_sha256"`
	StagedSchema  string                 `json:"staged_schema_sha256"`
	Requirements  []SchemaRequirement    `json:"requirements"`
	Unchanged     []nativeevidence.Table `json:"unchanged_user_tables"`
}

// RecoveryPreservationProof records derived data comparisons. Guest-code and
// callable/lifecycle/scheduler reviews remain separate required evidence.
type RecoveryPreservationProof struct {
	value recoveryPreservationRecord
	raw   []byte
	hash  string
}

func (p RecoveryPreservationProof) Bytes() []byte  { return bytes.Clone(p.raw) }
func (p RecoveryPreservationProof) SHA256() string { return p.hash }
func (p RecoveryPreservationProof) Requirements() []SchemaRequirement {
	return slices.Clone(p.value.Requirements)
}

func CheckRecoveryPairPreservation(source, staged RecoveredPairs) (RecoveryPreservationProof, error) {
	if source.hash == "" || staged.hash == "" || source.value.Database != staged.value.Database || source.value.Publisher != staged.value.Publisher {
		return RecoveryPreservationProof{}, ErrRecoveryPairs
	}
	diff, err := CompareSchemas(source.schema, staged.schema)
	if err != nil || diff.Decision() != StagedValidationRequired {
		return RecoveryPreservationProof{}, ErrRecoveryPairs
	}
	left, leftErr := passiveBaseRows(source.value.Rows, source.value.Layout)
	right, rightErr := passiveBaseRows(staged.value.Rows, staged.value.Layout)
	if leftErr != nil || rightErr != nil || !slices.Equal(left, right) {
		return RecoveryPreservationProof{}, ErrRecoveryPairs
	}
	unchanged, err := unchangedRecoveryUserTables(source, staged)
	if err != nil {
		return RecoveryPreservationProof{}, err
	}
	value := recoveryPreservationRecord{
		Format: RecoveryPreservationFormat, Contract: RecoveryPairsContract, Database: source.value.Database, Publisher: source.value.Publisher,
		SourceNative: source.value.NativeSHA256, StagedNative: staged.value.NativeSHA256, SourcePairs: source.hash, StagedPairs: staged.hash,
		SourceProgram: source.value.Program, StagedProgram: staged.value.Program, SourceWasm: source.value.Wasm, StagedWasm: staged.value.Wasm,
		SourceSchema: source.value.Schema, StagedSchema: staged.value.Schema, Requirements: recoveryDataRequirements(diff), Unchanged: unchanged,
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > MaxRecoveryPairsBytes {
		return RecoveryPreservationProof{}, ErrRecoveryPairs
	}
	sum := sha256.Sum256(raw)
	return RecoveryPreservationProof{value: value, raw: raw, hash: hex.EncodeToString(sum[:])}, nil
}

func recoveryDataRequirements(diff SchemaDiff) []SchemaRequirement {
	return slices.DeleteFunc(diff.Requirements(), func(requirement SchemaRequirement) bool {
		return requirement != VerifyPreservedData && requirement != VerifyNewTableData && requirement != VerifyDefaultProjection
	})
}

func unchangedRecoveryUserTables(source, staged RecoveredPairs) ([]nativeevidence.Table, error) {
	view, err := canonicalView(source.schema)
	if err != nil {
		return nil, ErrRecoveryPairs
	}
	old, next := recoveryTableSummaries(source.native.Tables()), recoveryTableSummaries(staged.native.Tables())
	var preserved []nativeevidence.Table
	for _, name := range rowNames(namedRows(view["Tables"])) {
		if name == "recovery_left" || name == "recovery_right" {
			continue
		}
		table, present := old[name]
		other, recovered := next[name]
		if !present || !recovered || table != other {
			return nil, ErrRecoveryPairs
		}
		preserved = append(preserved, table)
	}
	return preserved, nil
}

func recoveryTableSummaries(tables []nativeevidence.Table) map[string]nativeevidence.Table {
	result := make(map[string]nativeevidence.Table, len(tables))
	for _, table := range tables {
		result[table.Name] = table
	}
	return result
}
