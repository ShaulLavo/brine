package archive

import (
	"encoding/json"
	"regexp"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

var nativeArchiveOperationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// TaskReceipt names retained archive completion. It provides no purge or
// remote deletion authority and does not permit a writer to resume.
type TaskReceipt struct {
	Format              string         `json:"format"`
	OperationID         string         `json:"operation_id"`
	PlanID              string         `json:"plan_id"`
	OriginalOperationID string         `json:"original_operation_id"`
	ArchiveID           data.ArchiveID `json:"archive_id"`
	ManifestSHA256      string         `json:"manifest_sha256"`
	CompletionSHA256    string         `json:"completion_sha256"`
	Outcome             string         `json:"outcome"`
}

func DecodeTaskReceipt(raw []byte) (TaskReceipt, error) {
	var receipt TaskReceipt
	if len(raw) == 0 || len(raw) > 4096 {
		return receipt, ErrRefused
	}
	if _, err := strictjson.Object(raw, "format", "operation_id", "plan_id", "original_operation_id", "archive_id", "manifest_sha256", "completion_sha256", "outcome"); err != nil || json.Unmarshal(raw, &receipt) != nil {
		return TaskReceipt{}, ErrRefused
	}
	if receipt.Format != "brine-native-archive-receipt-v1" || !nativeArchiveOperationID.MatchString(receipt.OperationID) || !digest.MatchString(receipt.PlanID) || !data.ValidID(string(receipt.ArchiveID)) || !data.ValidCatalogHash(receipt.ManifestSHA256) || !data.ValidCatalogHash(receipt.CompletionSHA256) {
		return TaskReceipt{}, ErrRefused
	}
	if receipt.OriginalOperationID != "" && (!nativeArchiveOperationID.MatchString(receipt.OriginalOperationID) || receipt.OriginalOperationID == receipt.OperationID) {
		return TaskReceipt{}, ErrRefused
	}
	if receipt.Outcome != "prepared" && receipt.Outcome != "retired" && receipt.Outcome != "abandoned_current_stopped" {
		return TaskReceipt{}, ErrRefused
	}
	return receipt, nil
}
