package spacetimedb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/ShaulLavo/brine/internal/nativeevidence"
)

const RecoveredPairsFormat = "brine-passive-recovery-pairs-v1"

type recoveredPairsRecord struct {
	Format        string                   `json:"format"`
	Contract      string                   `json:"contract"`
	Database      string                   `json:"database_identity"`
	Publisher     string                   `json:"publisher_identity"`
	Program       string                   `json:"program_wire_hash"`
	Wasm          string                   `json:"artifact_wasm_sha256"`
	Canonicalizer string                   `json:"canonicalizer"`
	Schema        string                   `json:"schema_sha256"`
	Marker        string                   `json:"schema_marker"`
	Offset        uint64                   `json:"committed_offset"`
	NativeSHA256  string                   `json:"native_evidence_sha256"`
	Layout        string                   `json:"layout"`
	Rows          []nativeevidence.PairRow `json:"rows"`
	NotePresent   bool                     `json:"note_present"`
}

// RecoveredPairs contains finite projection facts from decoded passive output.
// The owned runner, source seal and disposal proof establish their provenance.
type RecoveredPairs struct {
	value  recoveredPairsRecord
	native nativeevidence.Evidence
	schema CanonicalSchema
	raw    []byte
	hash   string
}

func (p RecoveredPairs) Bytes() []byte                    { return bytes.Clone(p.raw) }
func (p RecoveredPairs) SHA256() string                   { return p.hash }
func (p RecoveredPairs) Database() DatabaseIdentity       { return DatabaseIdentity(p.value.Database) }
func (p RecoveredPairs) Publisher() PublisherIdentity     { return PublisherIdentity(p.value.Publisher) }
func (p RecoveredPairs) CurrentModule() RuntimeModuleHash { return RuntimeModuleHash(p.value.Program) }
func (p RecoveredPairs) ArtifactSHA256() WasmSHA256       { return WasmSHA256(p.value.Wasm) }
func (p RecoveredPairs) SchemaSHA256() string             { return p.value.Schema }
func (p RecoveredPairs) NativeEvidenceSHA256() string     { return p.value.NativeSHA256 }
func (p RecoveredPairs) RowCount() uint64                 { return uint64(len(p.value.Rows)) }
func (p RecoveredPairs) Offset() uint64                   { return p.value.Offset }

func RecoveryPairsFromPassive(evidence nativeevidence.Evidence, registered CanonicalSchema) (RecoveredPairs, error) {
	program := evidence.Program()
	projection, present := evidence.ConfirmedPairsProjection()
	if !present || !validHexIdentity(evidence.SHA256()) || program.Kind != "wasm" || registered.Version() != CanonicalizerVersion ||
		!SupportsConfirmedRecoveryPairs(program.SHA256, registered.SHA256()) || validatePassivePairs(projection, registered) != nil ||
		validatePassiveControls(projection, evidence.OwnerIdentity()) != nil || validateRecoveryUserCensus(evidence.Tables(), registered) != nil {
		return RecoveredPairs{}, ErrRecoveryPairs
	}
	rows := slices.Clone(projection.Left.Rows)
	slices.SortFunc(rows, recoverySequenceCompare)
	value := recoveredPairsRecord{
		Format: RecoveredPairsFormat, Contract: RecoveryPairsContract, Database: evidence.DatabaseIdentity(), Publisher: evidence.OwnerIdentity(),
		Program: program.Wire, Wasm: program.SHA256, Canonicalizer: registered.Version(), Schema: registered.SHA256(), Marker: RecoverySchemaMarker,
		Offset: evidence.Offset(), NativeSHA256: evidence.SHA256(), Layout: projection.Left.Layout, Rows: rows, NotePresent: projection.Note != nil,
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > MaxRecoveryPairsBytes {
		return RecoveredPairs{}, ErrRecoveryPairs
	}
	sum := sha256.Sum256(raw)
	return RecoveredPairs{value: value, native: evidence, schema: registered, raw: raw, hash: hex.EncodeToString(sum[:])}, nil
}

func validateRecoveryUserCensus(tables []nativeevidence.Table, schema CanonicalSchema) error {
	view, err := canonicalView(schema)
	if err != nil {
		return ErrRecoveryPairs
	}
	declared := namedRows(view["Tables"])
	users := 0
	for _, table := range tables {
		if table.Type != "user" {
			continue
		}
		if declared[table.Name] == nil {
			return ErrRecoveryPairs
		}
		users++
	}
	if users != len(declared) {
		return ErrRecoveryPairs
	}
	return nil
}

func validatePassiveControls(value nativeevidence.ConfirmedPairsProjection, owner string) error {
	if len(value.Publisher.Rows) != 1 || value.Publisher.Rows[0].ID != 1 || value.Publisher.Rows[0].Identity != owner ||
		len(value.Sequence.Rows) != 1 || value.Sequence.Rows[0].ID != 1 || value.Sequence.Rows[0].Sequence != uint64(len(value.Left.Rows)) {
		return ErrRecoveryPairs
	}
	return nil
}

func validatePassivePairs(value nativeevidence.ConfirmedPairsProjection, schema CanonicalSchema) error {
	view, err := canonicalView(schema)
	if err != nil || value.Format != nativeevidence.ProjectionFormat || len(value.Marker.Rows) != 1 || value.Marker.Rows[0].ID != 1 || value.Marker.Rows[0].Revision != RecoverySchemaMarker {
		return ErrRecoveryPairs
	}
	tables := namedRows(view["Tables"])
	layout, err := recoverySchemaLayout(tables)
	if err != nil || value.Left.Layout != layout || value.Right.Layout != layout || (value.Note != nil) != (tables["fixture_note"] != nil) {
		return ErrRecoveryPairs
	}
	if value.Note != nil && len(value.Note.Rows) != 0 {
		return ErrRecoveryPairs
	}
	left, err := passiveBaseRows(value.Left.Rows, layout)
	if err != nil {
		return err
	}
	right, err := passiveBaseRows(value.Right.Rows, layout)
	if err != nil || !slices.Equal(left, right) {
		return ErrRecoveryPairs
	}
	return nil
}

func recoverySchemaLayout(tables map[string]map[string]any) (string, error) {
	left, right := tables["recovery_left"], tables["recovery_right"]
	if left == nil || right == nil {
		return "", ErrRecoveryPairs
	}
	leftFields, rightFields := productFields(left), productFields(right)
	if len(leftFields) == 4 && len(rightFields) == 4 {
		return "base-v1", nil
	}
	if len(leftFields) != 5 || len(rightFields) != 5 || defaultColumns(left)[4] != "07" || defaultColumns(right)[4] != "07" {
		return "", ErrRecoveryPairs
	}
	return "rating-v1", nil
}

func passiveBaseRows(rows []nativeevidence.PairRow, layout string) ([]recoveryPair, error) {
	if rows == nil || len(rows) > MaxRecoveryPairs {
		return nil, ErrRecoveryPairs
	}
	owned := slices.Clone(rows)
	slices.SortFunc(owned, recoverySequenceCompare)
	result := make([]recoveryPair, 0, len(owned))
	for _, row := range owned {
		if !passiveRatingMatches(row.Rating, layout) {
			return nil, ErrRecoveryPairs
		}
		result = append(result, recoveryPair{Sequence: strconv.FormatUint(row.Sequence, 10), Marker: row.Marker, CommitTime: strconv.FormatInt(row.CommitTimeMicros, 10), Scheduled: row.Scheduled})
	}
	if validateRecoveryPairs(result) != nil {
		return nil, ErrRecoveryPairs
	}
	return result, nil
}

func passiveRatingMatches(rating *uint8, layout string) bool {
	if layout == "base-v1" {
		return rating == nil
	}
	return layout == "rating-v1" && rating != nil && *rating == 7
}

func recoverySequenceCompare(left, right nativeevidence.PairRow) int {
	if left.Sequence < right.Sequence {
		return -1
	}
	if left.Sequence > right.Sequence {
		return 1
	}
	return 0
}

func CheckConfirmedLowerBound(confirmed ConfirmedPairsRecord, final RecoveredPairs) error {
	if confirmed.hash == "" || final.hash == "" || confirmed.value.Database != final.value.Database || confirmed.value.Publisher != final.value.Publisher ||
		confirmed.value.Program != final.value.Program || confirmed.value.Wasm != final.value.Wasm || confirmed.value.Schema != final.value.Schema {
		return ErrRecoveryPairs
	}
	rows, err := passiveBaseRows(final.value.Rows, final.value.Layout)
	if err != nil || len(rows) < len(confirmed.value.Rows) || !slices.Equal(confirmed.value.Rows, rows[:len(confirmed.value.Rows)]) {
		return ErrRecoveryPairs
	}
	return nil
}

func CheckRecoveredPairsEqual(source, restored RecoveredPairs) error {
	if source.hash == "" || restored.hash == "" || source.value.Database != restored.value.Database || source.value.Publisher != restored.value.Publisher ||
		source.value.Program != restored.value.Program || source.value.Wasm != restored.value.Wasm || source.value.Schema != restored.value.Schema ||
		source.value.Offset != restored.value.Offset || source.value.Layout != restored.value.Layout || source.value.NotePresent != restored.value.NotePresent {
		return ErrRecoveryPairs
	}
	left, leftErr := passiveBaseRows(source.value.Rows, source.value.Layout)
	right, rightErr := passiveBaseRows(restored.value.Rows, restored.value.Layout)
	if leftErr != nil || rightErr != nil || !slices.Equal(left, right) {
		return ErrRecoveryPairs
	}
	return nil
}
