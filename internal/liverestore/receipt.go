package liverestore

import (
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

// Receipt reports retained completion. It is expectation data, never permission
// to start a restored engine or release its original fence.
type Receipt struct {
	Format                string                `json:"format"`
	OperationID           string                `json:"operation_id"`
	PlanID                string                `json:"plan_id"`
	OriginalOperationID   string                `json:"original_operation_id"`
	BindingID             data.ReplicaBindingID `json:"binding_id"`
	SelectedPointID       string                `json:"selected_point_id"`
	EpochID               data.ReplicaEpochID   `json:"epoch_id"`
	CompletionProofSHA256 string                `json:"completion_proof_sha256"`
	Outcome               string                `json:"outcome"`
}

func DecodeReceipt(raw []byte) (Receipt, error) {
	var r Receipt
	if len(raw) == 0 || len(raw) > RequestLimit {
		return r, ErrRecord
	}
	if _, err := strictjson.Object(raw, "format", "operation_id", "plan_id", "original_operation_id", "binding_id", "selected_point_id", "epoch_id", "completion_proof_sha256", "outcome"); err != nil || json.Unmarshal(raw, &r) != nil {
		return Receipt{}, ErrRecord
	}
	if r.Format != "brine-native-live-restore-receipt-v1" || !operationID.MatchString(r.OperationID) || !planID(r.PlanID) || !data.ValidID(string(r.BindingID)) || !data.ValidID(r.SelectedPointID) || !data.ValidID(string(r.EpochID)) || !digest(r.CompletionProofSHA256) {
		return Receipt{}, ErrRecord
	}
	if r.OriginalOperationID != "" && (!operationID.MatchString(r.OriginalOperationID) || r.OriginalOperationID == r.OperationID) {
		return Receipt{}, ErrRecord
	}
	if r.Outcome != "restored_stopped" && r.Outcome != "abandoned_current_stopped" {
		return Receipt{}, ErrRecord
	}
	return r, nil
}
