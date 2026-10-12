package datapublish

import (
	"encoding/json"
)

// TaskReceipt reports the protected controller's terminal result. Decoding it
// checks public metadata only, never publication or fence-release authority.
type TaskReceipt struct {
	Format           string `json:"format"`
	Kind             string `json:"kind"`
	OperationID      string `json:"operation_id"`
	ApplyOperationID string `json:"apply_operation_id"`
	PlanID           string `json:"plan_id"`
	PointID          string `json:"point_id"`
	BindingID        string `json:"binding_id"`
	OldEpochID       string `json:"old_epoch_id"`
	NewEpochID       string `json:"new_epoch_id"`
	TransitionID     string `json:"transition_id"`
	TransitionSHA256 string `json:"transition_sha256"`
	TransitionState  string `json:"transition_state"`
	CompletionSHA256 string `json:"completion_sha256"`
	Outcome          string `json:"outcome"`
	NativeDatabase   string `json:"native_database"`
	NativePublisher  string `json:"native_publisher"`
	ProgramWireHash  string `json:"program_wire_hash"`
	WasmSHA256       string `json:"wasm_sha256"`
	SchemaSHA256     string `json:"schema_sha256"`
}

func DecodeTaskReceipt(raw []byte) (TaskReceipt, error) {
	var receipt TaskReceipt
	if len(raw) == 0 || len(raw) > 16384 {
		return receipt, ErrRecord
	}
	fields := []string{"format", "kind", "operation_id", "apply_operation_id", "plan_id", "point_id", "binding_id", "old_epoch_id", "new_epoch_id", "transition_id", "transition_sha256", "transition_state", "completion_sha256", "outcome", "native_database", "native_publisher", "program_wire_hash", "wasm_sha256", "schema_sha256"}
	if _, err := objectValues(raw, fields...); err != nil || json.Unmarshal(raw, &receipt) != nil {
		return receipt, ErrRecord
	}
	if receipt.Format != "brine-module-publication-task-v1" || receipt.Kind != "module_publish_apply" && receipt.Kind != "module_publish_resolve" || !operationReference.MatchString(receipt.OperationID) || !operationReference.MatchString(receipt.ApplyOperationID) || !planID(receipt.PlanID) || !identity(receipt.PointID) || !identity(receipt.BindingID) || !identity(receipt.OldEpochID) || !identity(receipt.NewEpochID) || receipt.OldEpochID == receipt.NewEpochID || !digest(receipt.CompletionSHA256) {
		return TaskReceipt{}, ErrRecord
	}
	if receipt.Kind == "module_publish_apply" && receipt.ApplyOperationID != receipt.OperationID || receipt.Kind == "module_publish_resolve" && receipt.ApplyOperationID == receipt.OperationID {
		return TaskReceipt{}, ErrRecord
	}
	if receipt.Outcome == "source_preserved" {
		if receipt.Kind != "module_publish_resolve" || receipt.NativeDatabase != "" || receipt.NativePublisher != "" || receipt.ProgramWireHash != "" || receipt.WasmSHA256 != "" || receipt.SchemaSHA256 != "" {
			return TaskReceipt{}, ErrRecord
		}
		if !validPreservedPublicationTransition(receipt) {
			return TaskReceipt{}, ErrRecord
		}
		return receipt, nil
	}
	if receipt.Outcome != "published" || receipt.TransitionState != "completed" || !identity(receipt.TransitionID) || !digest(receipt.TransitionSHA256) || !digest(receipt.NativeDatabase) || !digest(receipt.NativePublisher) || !digest(receipt.ProgramWireHash) || !digest(receipt.WasmSHA256) || !digest(receipt.SchemaSHA256) {
		return TaskReceipt{}, ErrRecord
	}
	return receipt, nil
}

func validPreservedPublicationTransition(receipt TaskReceipt) bool {
	if receipt.TransitionState == "not_admitted" {
		return receipt.TransitionID == "" && receipt.TransitionSHA256 == ""
	}
	return receipt.TransitionState == "abandoned" && identity(receipt.TransitionID) && digest(receipt.TransitionSHA256)
}
