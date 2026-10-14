package nativecapture

import (
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

type AbandonReceipt struct {
	Kind                   string    `json:"kind"`
	OperationID            string    `json:"operation_id"`
	InputSHA256            string    `json:"input_sha256"`
	PointID                string    `json:"point_id"`
	PointSHA256            string    `json:"point_sha256"`
	PreparationOperationID string    `json:"preparation_operation_id"`
	FenceOwnerOperationID  string    `json:"fence_owner_operation_id"`
	FenceID                string    `json:"fence_id"`
	BindingID              string    `json:"binding_id"`
	EpochID                string    `json:"epoch_id"`
	SourceSealSHA256       string    `json:"source_seal_sha256"`
	CurrentStopSHA256      string    `json:"current_stop_sha256"`
	CurrentCodeSHA256      string    `json:"current_code_sha256"`
	CurrentRoutesSHA256    string    `json:"current_routes_sha256"`
	ReleaseID              string    `json:"release_id"`
	StoppedDesiredRevision uint64    `json:"stopped_desired_revision"`
	CompletedAt            time.Time `json:"completed_at"`
	Outcome                string    `json:"outcome"`
}

func DecodeAbandonReceipt(raw []byte) (AbandonReceipt, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return AbandonReceipt{}, ErrLimit
	}
	if _, err := strictjson.Object(raw, "kind", "operation_id", "input_sha256", "point_id", "point_sha256", "preparation_operation_id", "fence_owner_operation_id", "fence_id", "binding_id", "epoch_id", "source_seal_sha256", "current_stop_sha256", "current_code_sha256", "current_routes_sha256", "release_id", "stopped_desired_revision", "completed_at", "outcome"); err != nil {
		return AbandonReceipt{}, ErrInvalid
	}
	var receipt AbandonReceipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.Kind != "native_prepare_abandon" || receipt.Outcome != "abandoned_stopped" || receipt.StoppedDesiredRevision == 0 || receipt.StoppedDesiredRevision > 1<<63-1 || receipt.CompletedAt.IsZero() || receipt.CompletedAt.Location() != time.UTC {
		return AbandonReceipt{}, ErrInvalid
	}
	for _, id := range []string{receipt.OperationID, receipt.PreparationOperationID, receipt.FenceOwnerOperationID, receipt.ReleaseID} {
		if !taskOperationID.MatchString(id) {
			return AbandonReceipt{}, ErrInvalid
		}
	}
	for _, id := range []string{receipt.PointID, receipt.FenceID, receipt.BindingID, receipt.EpochID} {
		if !idPattern.MatchString(id) {
			return AbandonReceipt{}, ErrInvalid
		}
	}
	for _, hash := range []string{receipt.InputSHA256, receipt.PointSHA256, receipt.SourceSealSHA256, receipt.CurrentStopSHA256, receipt.CurrentCodeSHA256, receipt.CurrentRoutesSHA256} {
		if !hashPattern.MatchString(hash) {
			return AbandonReceipt{}, ErrInvalid
		}
	}
	return receipt, nil
}
