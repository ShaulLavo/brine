package nativecapture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

type EmptyScope struct {
	BindingID string `json:"binding_id"`
	EpochID   string `json:"epoch_id"`
}

type EmptyRecord struct {
	Format              string             `json:"format"`
	Scope               EmptyScope         `json:"scope"`
	GenerationID        string             `json:"generation_id"`
	ConfigurationSHA256 string             `json:"configuration_sha256"`
	PublisherReference  string             `json:"publisher_reference"`
	SigningRecovery     SigningRecovery    `json:"signing_recovery"`
	Members             []CheckpointMember `json:"members"`
}

// Source distinguishes structural unpublished allocation from initialized
// history. Neither arm asserts allocation, empty-state or replay authority.
type Source struct {
	kind       string
	empty      EmptyRecord
	checkpoint Checkpoint
	selection  Selection
	hash       string
}

func (s Source) Kind() string { return s.kind }
func (s Source) Empty() (EmptyRecord, bool) {
	record := s.empty
	record.Members = slices.Clone(record.Members)
	return record, s.kind == "unpublished"
}
func (s Source) Initialized() (Checkpoint, Selection, bool) {
	return s.checkpoint, s.selection, s.kind == "initialized"
}
func (s Source) SHA256() string { return s.hash }

func UnpublishedSource(record EmptyRecord, limits Limits) (Source, error) {
	if record.Format != Format || !idPattern.MatchString(record.Scope.BindingID) || !idPattern.MatchString(record.Scope.EpochID) || !idPattern.MatchString(record.GenerationID) || !hashPattern.MatchString(record.ConfigurationSHA256) || !validReference(record.PublisherReference) || !limits.valid() {
		return Source{}, ErrInvalid
	}
	if len(record.Members) == 0 || len(record.Members) > limits.Files {
		return Source{}, ErrLimit
	}
	record.Members = slices.Clone(record.Members)
	slices.SortFunc(record.Members, func(a, b CheckpointMember) int { return strings.Compare(a.Path, b.Path) })
	var total uint64
	for i, member := range record.Members {
		if member.Path != "config.toml" && member.Path != "metadata.toml" && member.Path != "recovery/signing.key" || !hashPattern.MatchString(member.SHA256) || member.Bytes == 0 || i > 0 && record.Members[i-1].Path == member.Path {
			return Source{}, ErrInvalid
		}
		if member.Bytes > limits.FileBytes {
			return Source{}, ErrLimit
		}
		total += member.Bytes
		if total > limits.Bytes {
			return Source{}, ErrLimit
		}
	}
	if !validRecovery(record.SigningRecovery, record.Members) {
		return Source{}, ErrInvalid
	}
	if !configurationMatches(record.ConfigurationSHA256, record.Members) {
		return Source{}, ErrInvalid
	}
	source := Source{kind: "unpublished", empty: record}
	raw, err := source.MarshalJSON()
	if err != nil {
		return Source{}, err
	}
	if len(raw) > 2<<20 || !sourceWithinLimits(record.Members, nil, uint64(len(raw)), limits) {
		return Source{}, ErrLimit
	}
	source.hash = sourceDigest(raw)
	return source, nil
}

func InitializedSource(checkpoint Checkpoint, selection Selection, limits Limits) (Source, error) {
	scope := checkpoint.record.Scope
	if !limits.valid() || checkpoint.hash == "" || !selection.scope.valid() || scope.BindingID != selection.scope.BindingID || scope.EpochID != selection.scope.EpochID || scope.ReplicaID != selection.scope.ReplicaID || len(selection.members) == 0 {
		return Source{}, ErrInvalid
	}
	source := Source{kind: "initialized", checkpoint: checkpoint, selection: selection}
	raw, err := source.MarshalJSON()
	if err != nil {
		return Source{}, err
	}
	if len(raw) > 2<<20 || !sourceWithinLimits(checkpoint.record.Members, selection.members, uint64(len(raw)), limits) {
		return Source{}, ErrLimit
	}
	source.hash = sourceDigest(raw)
	return source, nil
}

func sourceWithinLimits(checkpoint []CheckpointMember, selection []Member, encodedBytes uint64, limits Limits) bool {
	if len(checkpoint)+len(selection)+1 > limits.Files || encodedBytes > limits.FileBytes || encodedBytes > limits.ExpandedFileBytes {
		return false
	}
	stored, expanded := encodedBytes, encodedBytes
	for _, member := range checkpoint {
		if member.Bytes > limits.FileBytes || member.Bytes > limits.ExpandedFileBytes {
			return false
		}
		stored += member.Bytes
		expanded += member.Bytes
	}
	for _, member := range selection {
		if member.Bytes > limits.FileBytes || member.ExpandedBytes > limits.ExpandedFileBytes {
			return false
		}
		stored += member.Bytes
		expanded += member.ExpandedBytes
	}
	return stored <= limits.Bytes && expanded <= limits.ExpandedBytes
}

func sourceDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func validReference(reference string) bool {
	if len(reference) == 0 || len(reference) > 63 || reference[0] < 'a' || reference[0] > 'z' {
		return false
	}
	return strings.IndexFunc(reference, func(c rune) bool { return !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') }) == -1
}

func (s Source) MarshalJSON() ([]byte, error) {
	if s.kind == "unpublished" {
		return json.Marshal(struct {
			Kind  string      `json:"kind"`
			Empty EmptyRecord `json:"empty"`
		}{s.kind, s.empty})
	}
	if s.kind == "initialized" {
		return json.Marshal(struct {
			Kind          string          `json:"kind"`
			SelectionKind string          `json:"selection_kind"`
			Checkpoint    json.RawMessage `json:"checkpoint"`
			Scope         Scope           `json:"scope"`
			Members       []Member        `json:"members"`
		}{s.kind, s.selection.kind, s.checkpoint.encoded, s.selection.scope, s.selection.members})
	}
	return nil, ErrInvalid
}
