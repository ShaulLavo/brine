package nativecapture

import (
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"time"
)

type BackupResolutionReview struct {
	Format              string    `json:"format"`
	ID                  string    `json:"id"`
	OriginalOperationID string    `json:"original_operation_id"`
	OriginalKind        string    `json:"original_kind"`
	Requester           string    `json:"requester"`
	BindingID           string    `json:"binding_id"`
	OriginalSHA256      string    `json:"original_sha256"`
	ObservationSHA256   string    `json:"observation_sha256"`
	IssuedAt            time.Time `json:"issued_at"`
	ExpiresAt           time.Time `json:"expires_at"`
}

type BackupResolutionReceipt struct {
	Kind                string          `json:"kind"`
	OperationID         string          `json:"operation_id"`
	ReviewID            string          `json:"review_id"`
	OriginalOperationID string          `json:"original_operation_id"`
	OriginalKind        string          `json:"original_kind"`
	BindingID           string          `json:"binding_id"`
	EpochID             string          `json:"epoch_id"`
	OriginalSHA256      string          `json:"original_sha256"`
	ObservationSHA256   string          `json:"observation_sha256"`
	CompletionSHA256    string          `json:"completion_sha256"`
	Receipt             json.RawMessage `json:"receipt"`
	Outcome             string          `json:"outcome"`
}

func DecodeBackupResolutionReceipt(raw []byte) (BackupResolutionReceipt, error) {
	var r BackupResolutionReceipt
	fields := []string{"kind", "operation_id", "review_id", "original_operation_id", "original_kind", "binding_id", "epoch_id", "original_sha256", "observation_sha256", "completion_sha256", "receipt", "outcome"}
	if len(raw) == 0 || len(raw) > 16384 {
		return r, ErrLimit
	}
	if _, err := strictjson.Object(raw, fields...); err != nil {
		return r, ErrInvalid
	}
	if json.Unmarshal(raw, &r) != nil || r.Kind != "native_backup_resolve" || !taskOperationID.MatchString(r.OperationID) || !hashPattern.MatchString(r.ReviewID) || !taskOperationID.MatchString(r.OriginalOperationID) || !idPattern.MatchString(r.BindingID) || !idPattern.MatchString(r.EpochID) || !hashPattern.MatchString(r.OriginalSHA256) || !hashPattern.MatchString(r.ObservationSHA256) || !hashPattern.MatchString(r.CompletionSHA256) || r.Outcome != "adopted" {
		return r, ErrInvalid
	}
	if r.OriginalKind == "native_backup_activate" {
		original, err := DecodeBackupPolicyReceipt(r.Receipt)
		if err != nil || original.OperationID != r.OriginalOperationID || original.BindingID != r.BindingID || original.EpochID != r.EpochID {
			return r, ErrInvalid
		}
	} else {
		original, err := DecodeTaskReceipt(r.Receipt)
		if err != nil || original.Kind != r.OriginalKind || r.OriginalKind != "native_capture" && r.OriginalKind != "native_prepare" || original.OperationID != r.OriginalOperationID || original.BindingID != r.BindingID || original.EpochID != r.EpochID {
			return r, ErrInvalid
		}
	}
	return r, nil
}

func DecodeBackupResolutionReview(raw []byte) (BackupResolutionReview, error) {
	var r BackupResolutionReview
	fields := []string{"format", "id", "original_operation_id", "original_kind", "requester", "binding_id", "original_sha256", "observation_sha256", "issued_at", "expires_at"}
	if len(raw) == 0 || len(raw) > 16384 {
		return r, ErrLimit
	}
	if _, err := strictjson.Object(raw, fields...); err != nil {
		return r, ErrInvalid
	}
	if json.Unmarshal(raw, &r) != nil || r.Format != "brine-native-backup-resolution-review-v1" || !hashPattern.MatchString(r.ID) || !taskOperationID.MatchString(r.OriginalOperationID) || r.OriginalKind != "native_capture" && r.OriginalKind != "native_prepare" && r.OriginalKind != "native_backup_activate" || r.Requester == "" || len(r.Requester) > 253 || !idPattern.MatchString(r.BindingID) || !hashPattern.MatchString(r.OriginalSHA256) || !hashPattern.MatchString(r.ObservationSHA256) || r.IssuedAt.IsZero() || r.IssuedAt.Location() != time.UTC || r.ExpiresAt.Location() != time.UTC || !r.ExpiresAt.Equal(r.IssuedAt.Add(10*time.Minute)) {
		return r, ErrInvalid
	}
	identity := r
	identity.ID = ""
	body, _ := json.Marshal(identity)
	if sourceDigest(body) != r.ID {
		return r, ErrInvalid
	}
	return r, nil
}
