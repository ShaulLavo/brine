package nativecapture

import (
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

// PointInspection exposes protected references. It grants no native effect.
type PointInspection struct {
	Kind             string     `json:"kind"`
	PointID          string     `json:"point_id"`
	PointSHA256      string     `json:"point_sha256"`
	OperationID      string     `json:"operation_id"`
	BindingID        string     `json:"binding_id"`
	EpochID          string     `json:"epoch_id"`
	CheckpointSHA256 string     `json:"checkpoint_sha256"`
	SourceSHA256     string     `json:"source_sha256"`
	ObservedAt       time.Time  `json:"observed_at"`
	ExpiresAt        *time.Time `json:"expires_at"`
	RetainUntil      time.Time  `json:"retain_until"`
	Admission        string     `json:"admission"`
}

func (v PointInspection) Valid() bool {
	if !idPattern.MatchString(v.PointID) || !hashPattern.MatchString(v.PointSHA256) || !taskOperationID.MatchString(v.OperationID) || !idPattern.MatchString(v.BindingID) || !idPattern.MatchString(v.EpochID) || !hashPattern.MatchString(v.CheckpointSHA256) || !hashPattern.MatchString(v.SourceSHA256) || v.ObservedAt.IsZero() || v.ObservedAt.Location() != time.UTC || v.RetainUntil.Location() != time.UTC || v.RetainUntil.Before(v.ObservedAt) || v.Admission != "reference_only" {
		return false
	}
	if v.Kind == "verified" {
		return v.ExpiresAt == nil
	}
	return v.Kind == "prepared" && v.ExpiresAt != nil && v.ExpiresAt.Location() == time.UTC && v.ExpiresAt.After(v.ObservedAt)
}

func DecodePointInspection(raw []byte) (PointInspection, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return PointInspection{}, ErrLimit
	}
	if _, err := strictjson.Object(raw, "kind", "point_id", "point_sha256", "operation_id", "binding_id", "epoch_id", "checkpoint_sha256", "source_sha256", "observed_at", "expires_at", "retain_until", "admission"); err != nil {
		return PointInspection{}, ErrInvalid
	}
	var view PointInspection
	if json.Unmarshal(raw, &view) != nil || !view.Valid() {
		return PointInspection{}, ErrInvalid
	}
	return view, nil
}
