// Package nativeevidence decodes bounded passive SpacetimeDB state evidence.
package nativeevidence

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

const Format = "brine-native-reader-v1"
const UpstreamCommit = "6b6e0ab19e4f8f52472aa4379552e9593c8f53bb"
const Algorithm = "bsatn-row-digest-multiset-blake3-v1"

var ErrEvidence = errors.New("invalid native reader evidence")

type Limits struct {
	Files             uint64 `json:"files"`
	StoredBytes       uint64 `json:"stored_bytes"`
	FileBytes         uint64 `json:"file_bytes"`
	ExpandedBytes     uint64 `json:"expanded_bytes"`
	ExpandedFileBytes uint64 `json:"expanded_file_bytes"`
	CommitBytes       uint64 `json:"commit_bytes"`
	Commits           uint64 `json:"commits"`
	Tables            uint64 `json:"tables"`
	Rows              uint64 `json:"rows"`
	RowBytes          uint64 `json:"row_bytes"`
	StateBytes        uint64 `json:"state_bytes"`
	ProgramBytes      uint64 `json:"program_bytes"`
	OutputBytes       uint64 `json:"output_bytes"`
	MemoryBytes       uint64 `json:"memory_bytes"`
	AllocationBytes   uint64 `json:"allocation_bytes"`
	DeadlineMS        uint64 `json:"deadline_ms"`
}

func MaximumLimits() Limits {
	return Limits{8192, 256 << 20, 64 << 20, 256 << 20, 64 << 20, 8 << 20,
		1000000, 512, 100000, 8 << 20, 128 << 20, 8 << 20, 1 << 20, 384 << 20, 64 << 20, 60000}
}

type Program struct {
	Kind   string
	Digest string
	Wire   string
	SHA256 string
	Bytes  uint64
}

type Table struct {
	ID         uint32 `json:"table_id"`
	Name       string `json:"name"`
	Rows       uint64 `json:"rows"`
	BSATNBytes uint64 `json:"bsatn_bytes"`
	Blake3     string `json:"multiset_blake3"`
}

type Snapshot struct {
	Database string `json:"observed_database_identity"`
	Replica  uint64 `json:"observed_replica_id"`
	Offset   uint64 `json:"offset"`
}

type Totals struct {
	Files         uint64 `json:"files"`
	StoredBytes   uint64 `json:"stored_bytes"`
	ExpandedBytes uint64 `json:"expanded_bytes"`
	Commits       uint64 `json:"commits"`
}

type record struct {
	Format        string    `json:"format"`
	Outcome       string    `json:"outcome"`
	Upstream      string    `json:"upstream_commit"`
	Database      string    `json:"observed_database_identity"`
	Owner         string    `json:"observed_owner_identity"`
	Kind          string    `json:"current_program_kind"`
	ProgramAlgo   string    `json:"current_program_algorithm"`
	ProgramDigest string    `json:"current_program_digest"`
	ProgramWire   string    `json:"current_program_hash"`
	ProgramSHA    string    `json:"current_program_sha256"`
	ProgramBytes  uint64    `json:"program_bytes"`
	Offset        uint64    `json:"committed_offset"`
	Framing       string    `json:"framing"`
	Snapshot      *Snapshot `json:"snapshot"`
	Clients       uint64    `json:"persisted_connected_clients"`
	Algorithm     string    `json:"evidence_algorithm"`
	State         string    `json:"state_blake3"`
	Tables        []Table   `json:"tables"`
	Input         Totals    `json:"input"`
	Limits        Limits    `json:"limits"`
}

type Evidence struct{ value record }

func (e Evidence) DatabaseIdentity() string { return e.value.Database }
func (e Evidence) OwnerIdentity() string    { return e.value.Owner }
func (e Evidence) Program() Program {
	return Program{e.value.Kind, e.value.ProgramDigest, e.value.ProgramWire, e.value.ProgramSHA, e.value.ProgramBytes}
}
func (e Evidence) Offset() uint64                    { return e.value.Offset }
func (e Evidence) Tables() []Table                   { return slices.Clone(e.value.Tables) }
func (e Evidence) InputTotals() Totals               { return e.value.Input }
func (e Evidence) RequestedLimits() Limits           { return e.value.Limits }
func (e Evidence) StateBlake3() string               { return e.value.State }
func (e Evidence) PersistedConnectedClients() uint64 { return e.value.Clients }
func (e Evidence) Snapshot() (Snapshot, bool) {
	if e.value.Snapshot == nil {
		return Snapshot{}, false
	}
	return *e.value.Snapshot, true
}

func (e Evidence) SamePersistedState(other Evidence) bool {
	if e.value.Format != Format || other.value.Format != Format {
		return false
	}
	if e.value.Database != other.value.Database || e.value.Owner != other.value.Owner || e.Program() != other.Program() {
		return false
	}
	if e.value.Offset != other.value.Offset || e.value.Framing != other.value.Framing || e.value.Algorithm != other.value.Algorithm || e.value.State != other.value.State || e.value.Clients != other.value.Clients {
		return false
	}
	left, leftPresent := e.Snapshot()
	right, rightPresent := other.Snapshot()
	if leftPresent != rightPresent || left != right {
		return false
	}
	return slices.Equal(e.value.Tables, other.value.Tables)
}

func Decode(raw []byte, requested Limits) (Evidence, error) {
	if !validLimits(requested) || uint64(len(raw)) > requested.OutputBytes || len(raw) == 0 {
		return Evidence{}, ErrEvidence
	}
	fields, err := strictjson.Object(raw, "format", "outcome", "upstream_commit", "observed_database_identity", "observed_owner_identity", "current_program_kind", "current_program_algorithm", "current_program_digest", "current_program_hash", "current_program_sha256", "program_bytes", "committed_offset", "framing", "snapshot", "persisted_connected_clients", "evidence_algorithm", "state_blake3", "tables", "input", "limits")
	if err != nil || requiredValues(fields) != nil {
		return Evidence{}, ErrEvidence
	}
	if err := nested(fields, requested); err != nil {
		return Evidence{}, ErrEvidence
	}
	var value record
	if err := json.Unmarshal(raw, &value); err != nil || validate(value, requested) != nil {
		return Evidence{}, ErrEvidence
	}
	value.Tables = slices.Clone(value.Tables)
	if value.Snapshot != nil {
		owned := *value.Snapshot
		value.Snapshot = &owned
	}
	return Evidence{value}, nil
}

func requiredValues(fields map[string]json.RawMessage) error {
	for key, value := range fields {
		if key != "snapshot" && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrEvidence
		}
	}
	return nil
}

func object(raw []byte, fields ...string) error {
	values, err := strictjson.Object(raw, fields...)
	if err != nil {
		return ErrEvidence
	}
	for _, value := range values {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrEvidence
		}
	}
	return nil
}

func nested(fields map[string]json.RawMessage, requested Limits) error {
	if err := object(fields["limits"], "files", "stored_bytes", "file_bytes", "expanded_bytes", "expanded_file_bytes", "commit_bytes", "commits", "tables", "rows", "row_bytes", "state_bytes", "program_bytes", "output_bytes", "memory_bytes", "allocation_bytes", "deadline_ms"); err != nil {
		return err
	}
	if err := object(fields["input"], "files", "stored_bytes", "expanded_bytes", "commits"); err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(fields["snapshot"]), []byte("null")) {
		if err := object(fields["snapshot"], "observed_database_identity", "observed_replica_id", "offset"); err != nil {
			return err
		}
	}
	var tables []json.RawMessage
	if err := json.Unmarshal(fields["tables"], &tables); err != nil || uint64(len(tables)) > requested.Tables {
		return ErrEvidence
	}
	for _, table := range tables {
		if err := object(table, "table_id", "name", "rows", "bsatn_bytes", "multiset_blake3"); err != nil {
			return err
		}
	}
	return nil
}

func validLimits(value Limits) bool {
	actual := []uint64{value.Files, value.StoredBytes, value.FileBytes, value.ExpandedBytes, value.ExpandedFileBytes, value.CommitBytes, value.Commits, value.Tables, value.Rows, value.RowBytes, value.StateBytes, value.ProgramBytes, value.OutputBytes, value.MemoryBytes, value.AllocationBytes, value.DeadlineMS}
	limit := MaximumLimits()
	maxima := []uint64{limit.Files, limit.StoredBytes, limit.FileBytes, limit.ExpandedBytes, limit.ExpandedFileBytes, limit.CommitBytes, limit.Commits, limit.Tables, limit.Rows, limit.RowBytes, limit.StateBytes, limit.ProgramBytes, limit.OutputBytes, limit.MemoryBytes, limit.AllocationBytes, limit.DeadlineMS}
	for index, count := range actual {
		if count == 0 || count > maxima[index] {
			return false
		}
	}
	return value.OutputBytes >= 256 && value.MemoryBytes >= 8<<20 && value.AllocationBytes <= value.MemoryBytes
}

func hash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validate(value record, requested Limits) error {
	if value.Format != Format || value.Outcome != "observed" || value.Upstream != UpstreamCommit || value.ProgramAlgo != "keccak-256" || value.Algorithm != Algorithm || value.Limits != requested {
		return ErrEvidence
	}
	if value.Kind != "wasm" && value.Kind != "js" {
		return ErrEvidence
	}
	for _, item := range []string{value.Database, value.Owner, value.ProgramDigest, value.ProgramWire, value.ProgramSHA, value.State} {
		if !hash(item) {
			return ErrEvidence
		}
	}
	digest, _ := hex.DecodeString(value.ProgramDigest)
	slices.Reverse(digest)
	if hex.EncodeToString(digest) != value.ProgramWire {
		return ErrEvidence
	}
	if value.ProgramBytes > requested.ProgramBytes || value.Clients > requested.Rows || !boundedInput(value.Input, requested) {
		return ErrEvidence
	}
	if !validFraming(value) {
		return ErrEvidence
	}
	if value.Snapshot != nil && (value.Snapshot.Database != value.Database || value.Snapshot.Replica == 0 || value.Snapshot.Offset > value.Offset) {
		return ErrEvidence
	}
	return validTables(value.Tables, requested)
}

func boundedInput(value Totals, limits Limits) bool {
	return value.Files > 0 && value.Files <= limits.Files && value.StoredBytes > 0 && value.StoredBytes <= limits.StoredBytes && value.StoredBytes <= value.Files*limits.FileBytes && value.ExpandedBytes > 0 && value.ExpandedBytes <= limits.ExpandedBytes && value.ExpandedBytes <= value.Files*limits.ExpandedFileBytes && value.Commits <= limits.Commits
}

func validFraming(value record) bool {
	switch value.Framing {
	case "complete-v1":
		return value.Input.Commits > 0 && value.Offset >= value.Input.Commits-1
	case "snapshot-only-v1":
		return value.Input.Commits == 0 && value.Snapshot != nil && value.Offset == value.Snapshot.Offset
	default:
		return false
	}
}

func validTables(tables []Table, limits Limits) error {
	if len(tables) == 0 || uint64(len(tables)) > limits.Tables {
		return ErrEvidence
	}
	var previous uint32
	var rows, count uint64
	module := false
	for _, table := range tables {
		if table.ID <= previous || len(table.Name) == 0 || len(table.Name) > 1024 || !hash(table.Blake3) {
			return ErrEvidence
		}
		if table.Rows > limits.Rows-rows || table.BSATNBytes > limits.StateBytes-count {
			return ErrEvidence
		}
		if table.Rows == 0 && table.BSATNBytes != 0 {
			return ErrEvidence
		}
		if table.BSATNBytes > table.Rows*limits.RowBytes {
			return ErrEvidence
		}
		rows += table.Rows
		count += table.BSATNBytes
		previous = table.ID
		if table.ID == 6 {
			module = table.Name == "st_module" && table.Rows == 1
		}
	}
	if !module {
		return ErrEvidence
	}
	return nil
}
