package ops

import (
	"encoding/json"
	"regexp"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

var initializationPlanID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var initializationReceiptID = regexp.MustCompile(`^[a-f0-9]{32}$`)

const MaxTaskOutcomeBytes = 65536

// TaskOutcome carries either a closed, typed receipt or a fixed safe error code.
// Subprocess output and credential packets have no representation here.
type TaskOutcome struct {
	Receipt json.RawMessage `json:"receipt,omitempty"`
	Error   result.Code     `json:"error,omitempty"`
}

func DecodeTaskReceipt(op Operation, raw json.RawMessage) (any, error) {
	switch op.Kind {
	case DataInitApply:
		if _, err := strictjson.Object(raw, "id", "plan_id", "state", "fence_id"); err != nil {
			return nil, strictjson.ErrObject
		}
		var receipt datainit.Operation
		if json.Unmarshal(raw, &receipt) != nil || !initializationReceiptID.MatchString(receipt.ID) || !initializationReceiptID.MatchString(string(receipt.Fence)) || receipt.PlanID != op.SecretRef || !initializationPlanID.MatchString(receipt.PlanID) || receipt.State != "succeeded" {
			return nil, strictjson.ErrObject
		}
		return receipt, nil
	case RestoreTest:
		receipt, err := restore.DecodeReceipt(raw)
		if err != nil || receipt.OperationID != op.ID {
			return nil, strictjson.ErrObject
		}
		return receipt, nil
	case CredentialActivation:
		receipt, err := backupcredentials.DecodeReceipt(raw)
		if err != nil || receipt.Scope.App != op.App || receipt.PlanID != op.SecretRef || receipt.Requester != op.Requester {
			return nil, strictjson.ErrObject
		}
		return receipt, nil
	default:
		return nil, strictjson.ErrObject
	}
}

func (o TaskOutcome) Valid(op Operation) bool {
	if !op.Kind.IsTask() || !op.State.IsTerminal() {
		return false
	}
	if o.Error != "" {
		return len(o.Receipt) == 0 && op.State != Succeeded && result.KnownCode(o.Error)
	}
	if op.State != Succeeded {
		return false
	}
	_, err := DecodeTaskReceipt(op, o.Receipt)
	return err == nil
}

func DecodeTaskOutcome(op Operation, raw []byte) (TaskOutcome, error) {
	if len(raw) == 0 || len(raw) > MaxTaskOutcomeBytes {
		return TaskOutcome{}, strictjson.ErrObject
	}
	var o TaskOutcome
	if json.Unmarshal(raw, &o) != nil {
		return TaskOutcome{}, strictjson.ErrObject
	}
	field := "receipt"
	if o.Error != "" {
		field = "error"
	}
	if _, err := strictjson.Object(raw, field); err != nil || !o.Valid(op) {
		return TaskOutcome{}, strictjson.ErrObject
	}
	return o, nil
}
