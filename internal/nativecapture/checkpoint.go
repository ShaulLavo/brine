package nativecapture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

// CheckpointRecord contains metadata and hashes only. Its decoder establishes
// a closed immutable record, not that its source was actually sealed/admitted.
type CheckpointRecord struct {
	Format              string             `json:"format"`
	Scope               CheckpointScope    `json:"scope"`
	GenerationID        string             `json:"generation_id"`
	DatabaseIdentity    string             `json:"database_identity"`
	PublisherIdentity   string             `json:"publisher_identity"`
	Names               []string           `json:"names"`
	RuntimeModuleHash   string             `json:"runtime_module_hash"`
	ArtifactSHA256      string             `json:"artifact_sha256"`
	SchemaVersion       string             `json:"schema_version"`
	SchemaSHA256        string             `json:"schema_sha256"`
	ConfigurationSHA256 string             `json:"configuration_sha256"`
	SigningRecovery     SigningRecovery    `json:"signing_recovery"`
	Members             []CheckpointMember `json:"members"`
}

type CheckpointMember struct {
	Path   string `json:"path"`
	Bytes  uint64 `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type CheckpointScope struct {
	BindingID string `json:"binding_id"`
	EpochID   string `json:"epoch_id"`
	ReplicaID uint64 `json:"replica_id"`
}

func (s CheckpointScope) valid() bool {
	return idPattern.MatchString(s.BindingID) && idPattern.MatchString(s.EpochID)
}

// SigningRecovery refers either to an encrypted archive member or to a tested
// immutable external secret version. It never contains keys or passwords.
type SigningRecovery struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
	Version   uint64 `json:"version"`
	SHA256    string `json:"sha256"`
	Member    string `json:"member"`
}

type Checkpoint struct {
	record  CheckpointRecord
	encoded []byte
	hash    string
}

func (c Checkpoint) SHA256() string           { return c.hash }
func (c Checkpoint) Bytes() []byte            { return slices.Clone(c.encoded) }
func (c Checkpoint) Record() CheckpointRecord { return cloneCheckpoint(c.record) }

func NewCheckpoint(record CheckpointRecord, limits Limits) (Checkpoint, error) {
	if !limits.valid() || record.Format != Format || !record.Scope.valid() || !idPattern.MatchString(record.GenerationID) || !hashPattern.MatchString(record.DatabaseIdentity) || !hashPattern.MatchString(record.PublisherIdentity) || !hashPattern.MatchString(record.RuntimeModuleHash) || !hashPattern.MatchString(record.ArtifactSHA256) || record.SchemaVersion != spacetimedb.CanonicalizerVersion || !hashPattern.MatchString(record.SchemaSHA256) || !hashPattern.MatchString(record.ConfigurationSHA256) {
		return Checkpoint{}, ErrInvalid
	}
	if len(record.Names) > 64 || len(record.Members) == 0 || len(record.Members) > limits.Files {
		return Checkpoint{}, ErrLimit
	}
	record = cloneCheckpoint(record)
	slices.Sort(record.Names)
	for i, name := range record.Names {
		if !validLogicalName(name) || i > 0 && record.Names[i-1] == name {
			return Checkpoint{}, ErrInvalid
		}
	}
	slices.SortFunc(record.Members, func(a, b CheckpointMember) int { return strings.Compare(a.Path, b.Path) })
	var total uint64
	for i, member := range record.Members {
		if !validCheckpointPath(member.Path) || !hashPattern.MatchString(member.SHA256) || member.Bytes == 0 || i > 0 && record.Members[i-1].Path == member.Path {
			return Checkpoint{}, ErrInvalid
		}
		if member.Bytes > limits.FileBytes {
			return Checkpoint{}, ErrLimit
		}
		total += member.Bytes
		if total > limits.Bytes {
			return Checkpoint{}, ErrLimit
		}
	}
	if !validRecovery(record.SigningRecovery, record.Members) {
		return Checkpoint{}, ErrInvalid
	}
	if !configurationMatches(record.ConfigurationSHA256, record.Members) {
		return Checkpoint{}, ErrInvalid
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 1<<20 {
		return Checkpoint{}, ErrLimit
	}
	sum := sha256.Sum256(raw)
	return Checkpoint{record: record, encoded: raw, hash: hex.EncodeToString(sum[:])}, nil
}

func cloneCheckpoint(record CheckpointRecord) CheckpointRecord {
	record.Names = slices.Clone(record.Names)
	record.Members = slices.Clone(record.Members)
	if record.Names == nil {
		record.Names = []string{}
	}
	return record
}

func validLogicalName(name string) bool {
	return len(name) > 0 && len(name) <= 253 && !strings.ContainsAny(name, "/\\\x00\r\n")
}

func validCheckpointPath(name string) bool {
	if path.Clean(name) != name || strings.HasPrefix(name, "/") || len(name) > 240 {
		return false
	}
	if name == "metadata.toml" || name == "config.toml" || name == "recovery/signing.key" {
		return true
	}
	return safeCheckpointSubpath(name, "control-db/") || safeCheckpointSubpath(name, "program-bytes/")
}

func safeCheckpointSubpath(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) || len(name) == len(prefix) {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(name, prefix), "/") {
		if part == "" || part == ".." || strings.IndexFunc(part, invalidStorageRune) != -1 {
			return false
		}
	}
	return true
}

func invalidStorageRune(c rune) bool {
	return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_')
}

func validRecovery(recovery SigningRecovery, members []CheckpointMember) bool {
	if !validReference(recovery.Reference) || recovery.Version == 0 || !hashPattern.MatchString(recovery.SHA256) {
		return false
	}
	var memberHash string
	for _, member := range members {
		if member.Path == "recovery/signing.key" {
			memberHash = member.SHA256
		}
	}
	if recovery.Kind == "external_immutable" {
		return recovery.Member == "" && memberHash == ""
	}
	return recovery.Kind == "encrypted_member" && recovery.Member == "recovery/signing.key" && memberHash == recovery.SHA256
}

func configurationMatches(expected string, members []CheckpointMember) bool {
	for _, member := range members {
		if member.Path == "config.toml" {
			return member.SHA256 == expected
		}
	}
	return false
}

func CheckGeneration(checkpoint Checkpoint, current Checkpoint) error {
	if checkpoint.hash == "" || current.hash == "" || checkpoint.hash != current.hash {
		return ErrDrift
	}
	return nil
}
