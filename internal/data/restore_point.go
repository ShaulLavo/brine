package data

import (
	"path"
	"strings"
	"time"
)

type RestorePointKind string

const (
	RestorePointLTX      RestorePointKind = "litestream_ltx"
	RestorePointSnapshot RestorePointKind = "sqlite_snapshot"
)

// RestorePoint is immutable preparation evidence, not a recoverability-test
// receipt. An exact LTX point always requires a committed upload barrier.
type RestorePoint struct {
	ID         string                `json:"id"`
	BindingID  ReplicaBindingID      `json:"binding_id"`
	EpochID    ReplicaEpochID        `json:"epoch_id"`
	Kind       RestorePointKind      `json:"kind"`
	Schema     SchemaObservation     `json:"schema"`
	LTX        *RestoreLTXPoint      `json:"litestream_ltx,omitempty"`
	Snapshot   *RestoreSnapshotPoint `json:"sqlite_snapshot,omitempty"`
	RecordedAt time.Time             `json:"recorded_at"`
}
type RestoreLTXPoint struct {
	TXID    uint64               `json:"txid"`
	Barrier RestoreUploadBarrier `json:"barrier"`
}
type RestoreUploadBarrier struct {
	BindingID   ReplicaBindingID `json:"binding_id"`
	EpochID     ReplicaEpochID   `json:"epoch_id"`
	TXID        uint64           `json:"txid"`
	ReplicaTXID uint64           `json:"replica_txid"`
	ObservedAt  time.Time        `json:"observed_at"`
}
type RestoreSnapshotPoint struct {
	ObjectKey string `json:"object_key"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

func (p RestorePoint) Validate() error {
	if !ValidID(p.ID) || !ValidID(string(p.BindingID)) || !ValidID(string(p.EpochID)) || p.RecordedAt.IsZero() || !ValidID(string(p.Schema.DatabaseID)) || p.Schema.ObservedAt.IsZero() || p.Schema.ObservedAt.After(p.RecordedAt) || p.Schema.UnknownReason != "" || !ValidCatalogHash(p.Schema.CatalogSHA256) {
		return ErrInvalid
	}
	switch p.Schema.State {
	case VerifiedEmpty:
		if p.Schema.Marker != EmptyMarker || p.Schema.CatalogSHA256 != EmptyCatalogSHA256 {
			return ErrInvalid
		}
	case VerifiedSchema:
		if !ValidMarker(p.Schema.Marker) || p.Schema.Marker == EmptyMarker {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	switch p.Kind {
	case RestorePointLTX:
		if p.LTX == nil || p.Snapshot != nil || p.LTX.TXID == 0 {
			return ErrInvalid
		}
		b := p.LTX.Barrier
		if b.BindingID != p.BindingID || b.EpochID != p.EpochID || b.TXID != p.LTX.TXID || b.ReplicaTXID < b.TXID || b.ObservedAt.IsZero() || b.ObservedAt.After(p.RecordedAt) {
			return ErrInvalid
		}
	case RestorePointSnapshot:
		if p.Snapshot == nil || p.LTX != nil || !ValidCatalogHash(p.Snapshot.SHA256) || p.Snapshot.Size <= 0 || p.Snapshot.Size > 1<<40 {
			return ErrInvalid
		}
		key := p.Snapshot.ObjectKey
		if len(key) > 1024 || key == "" || path.Clean(key) != key || path.IsAbs(key) || strings.HasPrefix(key, "../") || strings.ContainsAny(key, "\\\x00\r\n") {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
