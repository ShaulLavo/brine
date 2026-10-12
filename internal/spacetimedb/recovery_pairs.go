package spacetimedb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

const ConfirmedPairsFormat = "brine-confirmed-recovery-pairs-v1"
const RecoveryPairsContract = "brine-recovery-pairs-confirmed-v1"
const RecoverySchemaMarker = "brine-recovery-v1"
const MaxRecoveryPairs = 10_000
const MaxRecoveryPairsBytes = 1 << 20

var ErrRecoveryPairs = errors.New("SpacetimeDB recovery-pairs evidence refused")

type recoveryPair struct {
	Sequence   string `json:"sequence"`
	Marker     string `json:"marker"`
	CommitTime string `json:"commit_time_micros"`
	Scheduled  bool   `json:"scheduled"`
}

type confirmedPairsRecord struct {
	Format        string         `json:"format"`
	Contract      string         `json:"contract"`
	Basis         string         `json:"basis"`
	Database      string         `json:"database_identity"`
	Publisher     string         `json:"publisher_identity"`
	Names         []string       `json:"names"`
	Program       string         `json:"program_wire_hash"`
	Wasm          string         `json:"artifact_wasm_sha256"`
	Canonicalizer string         `json:"canonicalizer"`
	Schema        string         `json:"schema_sha256"`
	Marker        string         `json:"schema_marker"`
	Rows          []recoveryPair `json:"rows"`
}

// ConfirmedPairsRecord is retained syntax. Decoding never proves source ownership
// or a native read; the host authenticates its protected journal separately.
type ConfirmedPairsRecord struct {
	value confirmedPairsRecord
	raw   []byte
	hash  string
}

func (r ConfirmedPairsRecord) Bytes() []byte              { return bytes.Clone(r.raw) }
func (r ConfirmedPairsRecord) SHA256() string             { return r.hash }
func (r ConfirmedPairsRecord) Database() DatabaseIdentity { return DatabaseIdentity(r.value.Database) }
func (r ConfirmedPairsRecord) Publisher() PublisherIdentity {
	return PublisherIdentity(r.value.Publisher)
}
func (r ConfirmedPairsRecord) CurrentModule() RuntimeModuleHash {
	return RuntimeModuleHash(r.value.Program)
}
func (r ConfirmedPairsRecord) ArtifactSHA256() WasmSHA256 { return WasmSHA256(r.value.Wasm) }
func (r ConfirmedPairsRecord) SchemaSHA256() string       { return r.value.Schema }
func (r ConfirmedPairsRecord) RowCount() uint64           { return uint64(len(r.value.Rows)) }
func (r ConfirmedPairsRecord) Names() []string            { return slices.Clone(r.value.Names) }

// ConfirmedPairsObservation has no constructor. Only the fixed native observer
// creates it; its durable lower bound is not the final stopped-source state.
type ConfirmedPairsObservation struct{ record ConfirmedPairsRecord }

func (o ConfirmedPairsObservation) Record() ConfirmedPairsRecord { return o.record }

func DecodeConfirmedPairsRecord(raw []byte) (ConfirmedPairsRecord, error) {
	if len(raw) == 0 || len(raw) > MaxRecoveryPairsBytes {
		return ConfirmedPairsRecord{}, ErrRecoveryPairs
	}
	fields, err := strictjson.Object(raw, "format", "contract", "basis", "database_identity", "publisher_identity", "names", "program_wire_hash", "artifact_wasm_sha256", "canonicalizer", "schema_sha256", "schema_marker", "rows")
	if err != nil || validateConfirmedRecordFields(fields) != nil {
		return ConfirmedPairsRecord{}, ErrRecoveryPairs
	}
	value, err := strictjson.Value[confirmedPairsRecord](raw)
	if err != nil {
		return ConfirmedPairsRecord{}, ErrRecoveryPairs
	}
	result, err := newConfirmedPairsRecord(value)
	if err != nil || !bytes.Equal(raw, result.raw) {
		return ConfirmedPairsRecord{}, ErrRecoveryPairs
	}
	return result, nil
}

func validateConfirmedRecordFields(fields map[string]json.RawMessage) error {
	for _, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return ErrRecoveryPairs
		}
	}
	var rows []json.RawMessage
	if json.Unmarshal(fields["rows"], &rows) != nil || len(rows) > MaxRecoveryPairs {
		return ErrRecoveryPairs
	}
	for _, raw := range rows {
		if _, err := strictjson.Object(raw, "sequence", "marker", "commit_time_micros", "scheduled"); err != nil {
			return ErrRecoveryPairs
		}
	}
	return nil
}

func newConfirmedPairsRecord(value confirmedPairsRecord) (ConfirmedPairsRecord, error) {
	if value.Format != ConfirmedPairsFormat || value.Contract != RecoveryPairsContract || value.Basis != "confirmed_sql_lower_bound" ||
		value.Canonicalizer != CanonicalizerVersion || value.Marker != RecoverySchemaMarker || !validHexIdentity(value.Database) ||
		!validHexIdentity(value.Publisher) || !validHexIdentity(value.Program) || !SupportsConfirmedRecoveryPairs(value.Wasm, value.Schema) {
		return ConfirmedPairsRecord{}, ErrRecoveryPairs
	}
	names, err := normalizedNames(value.Names)
	if err != nil || len(names) == 0 || len(names) > 16 || !slices.Equal(value.Names, names) || validateRecoveryPairs(value.Rows) != nil {
		return ConfirmedPairsRecord{}, ErrRecoveryPairs
	}
	value.Names, value.Rows = names, slices.Clone(value.Rows)
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > MaxRecoveryPairsBytes {
		return ConfirmedPairsRecord{}, ErrRecoveryPairs
	}
	sum := sha256.Sum256(raw)
	return ConfirmedPairsRecord{value: value, raw: raw, hash: hex.EncodeToString(sum[:])}, nil
}

func validateRecoveryPairs(rows []recoveryPair) error {
	if rows == nil || len(rows) > MaxRecoveryPairs {
		return ErrRecoveryPairs
	}
	for index, row := range rows {
		if row.Sequence != strconv.Itoa(index+1) || row.Marker != fmt.Sprintf("brine-p09-%08d", index+1) {
			return ErrRecoveryPairs
		}
		micros, err := strconv.ParseInt(row.CommitTime, 10, 64)
		if err != nil || micros <= 0 || strconv.FormatInt(micros, 10) != row.CommitTime {
			return ErrRecoveryPairs
		}
	}
	return nil
}
