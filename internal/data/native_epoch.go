package data

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const NativeEpochFormat = "brine-native-epoch-v1"

type NativeEpochRecord struct {
	Engine               EngineKind       `json:"engine"`
	IncarnationID        AppIncarnationID `json:"incarnation_id"`
	DatabaseID           DatabaseID       `json:"database_id"`
	BindingID            ReplicaBindingID `json:"binding_id"`
	EpochID              ReplicaEpochID   `json:"epoch_id"`
	PredecessorEpochID   ReplicaEpochID   `json:"predecessor_epoch_id"`
	TransitionID         string           `json:"transition_id"`
	ArchiveBindingSHA256 string           `json:"archive_binding_sha256"`
	RepositoryID         string           `json:"repository_id"`
	ReservedAt           time.Time        `json:"reserved_at"`
}

func (r NativeEpochRecord) Validate() error {
	if r.Engine != EngineSpacetimeDB || !ValidID(string(r.IncarnationID)) || !ValidID(string(r.DatabaseID)) ||
		!ValidID(string(r.BindingID)) || !ValidID(string(r.EpochID)) || !ValidID(string(r.PredecessorEpochID)) ||
		r.EpochID == r.PredecessorEpochID || !ValidID(r.TransitionID) || !engineDigest(r.ArchiveBindingSHA256) ||
		!engineDigest(r.RepositoryID) || r.ReservedAt.IsZero() {
		return ErrInvalid
	}
	return nil
}

// NativeEpoch binds reservation metadata; it neither owns a destination nor admits a writer.
type NativeEpoch struct {
	record  NativeEpochRecord
	encoded []byte
	hash    string
}

func NewNativeEpoch(t EngineTransition, archiveBindingSHA256, repositoryID string, at time.Time) (NativeEpoch, error) {
	if t.hash == "" {
		return NativeEpoch{}, ErrInvalid
	}
	c := t.common
	record := NativeEpochRecord{
		Engine: c.Engine, IncarnationID: c.Scope.IncarnationID, DatabaseID: c.Scope.DatabaseID, BindingID: c.Scope.BindingID,
		EpochID: c.NewEpochID, PredecessorEpochID: c.Base.epoch(), TransitionID: c.Scope.ID,
		ArchiveBindingSHA256: archiveBindingSHA256, RepositoryID: repositoryID, ReservedAt: at,
	}
	return encodeNativeEpoch(record)
}

func (e NativeEpoch) Record() NativeEpochRecord { return e.record }
func (e NativeEpoch) SHA256() string            { return e.hash }
func (e NativeEpoch) Bytes() []byte             { return bytes.Clone(e.encoded) }

func (e NativeEpoch) Matches(t EngineTransition) bool {
	if e.hash == "" || t.hash == "" {
		return false
	}
	r, c := e.record, t.common
	return r.Engine == c.Engine && r.IncarnationID == c.Scope.IncarnationID && r.DatabaseID == c.Scope.DatabaseID &&
		r.BindingID == c.Scope.BindingID && r.EpochID == c.NewEpochID && r.PredecessorEpochID == c.Base.epoch() && r.TransitionID == c.Scope.ID
}

func encodeNativeEpoch(record NativeEpochRecord) (NativeEpoch, error) {
	if record.Validate() != nil {
		return NativeEpoch{}, ErrInvalid
	}
	raw, err := json.Marshal(map[string]any{"format": NativeEpochFormat, "reservation": record})
	if err != nil || len(raw) > MaxEngineTransitionBytes {
		return NativeEpoch{}, ErrInvalid
	}
	sum := sha256.Sum256(raw)
	return NativeEpoch{record: record, encoded: raw, hash: hex.EncodeToString(sum[:])}, nil
}

func (e NativeEpoch) MarshalJSON() ([]byte, error) {
	if e.hash == "" {
		return nil, ErrInvalid
	}
	return e.Bytes(), nil
}

func DecodeNativeEpoch(raw []byte) (NativeEpoch, error) {
	if len(raw) == 0 || len(raw) > MaxEngineTransitionBytes {
		return NativeEpoch{}, ErrInvalid
	}
	fields, err := engineRecordFields(raw, []string{"format", "reservation"}, nil)
	if err != nil {
		return NativeEpoch{}, err
	}
	var format string
	if json.Unmarshal(fields["format"], &format) != nil || format != NativeEpochFormat {
		return NativeEpoch{}, ErrInvalid
	}
	required := []string{"engine", "incarnation_id", "database_id", "binding_id", "epoch_id", "predecessor_epoch_id", "transition_id", "archive_binding_sha256", "repository_id", "reserved_at"}
	if engineRecord(fields["reservation"], required, nil) != nil {
		return NativeEpoch{}, ErrInvalid
	}
	var record NativeEpochRecord
	if json.Unmarshal(fields["reservation"], &record) != nil {
		return NativeEpoch{}, ErrInvalid
	}
	return encodeNativeEpoch(record)
}

func (e *NativeEpoch) UnmarshalJSON(raw []byte) error {
	decoded, err := DecodeNativeEpoch(raw)
	if err != nil {
		return err
	}
	*e = decoded
	return nil
}
