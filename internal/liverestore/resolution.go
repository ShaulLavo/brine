package liverestore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"time"
)

// Resolution binds fresh review to one retained original journal and layout.
type Resolution struct {
	Format              string             `json:"format"`
	OriginalOperationID string             `json:"original_operation_id"`
	ApplyPlanID         string             `json:"apply_plan_id"`
	Requester           datainit.Requester `json:"requester"`
	EventSHA256         string             `json:"event_sha256"`
	InputSHA256         string             `json:"input_sha256"`
	ObservationSHA256   string             `json:"observation_sha256"`
	IssuedAt            time.Time          `json:"issued_at"`
	ExpiresAt           time.Time          `json:"expires_at"`
}

func (r Resolution) ID() string {
	if !r.valid() {
		return ""
	}
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func (r Resolution) valid() bool {
	return r.Format == "brine-native-live-restore-resolution-v1" && operationID.MatchString(r.OriginalOperationID) && planID(r.ApplyPlanID) && r.Requester.Valid() && digest(r.EventSHA256) && digest(r.InputSHA256) && digest(r.ObservationSHA256) && utc(r.IssuedAt) && utc(r.ExpiresAt) && r.ExpiresAt.After(r.IssuedAt) && r.ExpiresAt.Sub(r.IssuedAt) <= MaxReviewLifetime
}
func DecodeResolution(raw []byte) (Resolution, error) {
	var r Resolution
	if len(raw) > RequestLimit {
		return r, ErrRecord
	}
	if _, err := strictjson.Object(raw, "format", "original_operation_id", "apply_plan_id", "requester", "event_sha256", "input_sha256", "observation_sha256", "issued_at", "expires_at"); err != nil || json.Unmarshal(raw, &r) != nil || !r.valid() {
		return Resolution{}, ErrRecord
	}
	canonical, _ := json.Marshal(r)
	if !bytes.Equal(canonical, raw) {
		return Resolution{}, ErrRecord
	}
	return r, nil
}
