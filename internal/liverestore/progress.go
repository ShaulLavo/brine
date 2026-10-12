package liverestore

import (
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type Progress struct {
	OperationID     string                `json:"operation_id"`
	PlanID          string                `json:"plan_id"`
	BindingID       data.ReplicaBindingID `json:"binding_id"`
	SelectedPointID string                `json:"selected_point_id"`
	IncomingEpochID data.ReplicaEpochID   `json:"incoming_epoch_id"`
	Stage           Stage                 `json:"stage"`
	Sequence        uint64                `json:"sequence"`
}

func ProgressFromEvent(e Event) Progress {
	a := e.Attempt()
	return Progress{OperationID: a.OperationID, PlanID: a.PlanID, BindingID: a.BindingID, SelectedPointID: a.SelectedPointID, IncomingEpochID: a.IncomingEpochID, Stage: e.Stage(), Sequence: e.Sequence()}
}

func DecodeProgress(raw []byte) (Progress, error) {
	var p Progress
	if len(raw) > RequestLimit {
		return p, ErrRecord
	}
	if _, err := strictjson.Object(raw, "operation_id", "plan_id", "binding_id", "selected_point_id", "incoming_epoch_id", "stage", "sequence"); err != nil || json.Unmarshal(raw, &p) != nil {
		return Progress{}, ErrRecord
	}
	if !operationID.MatchString(p.OperationID) || !planID(p.PlanID) || !data.ValidID(string(p.BindingID)) || !data.ValidID(p.SelectedPointID) || !data.ValidID(string(p.IncomingEpochID)) || p.Sequence == 0 {
		return Progress{}, ErrRecord
	}
	switch p.Stage {
	case Claimed, RestoreIntent, Verified, TransitionIntent, Transitioned, FinishIntent, Completed, AbandonIntent, AbandonedCurrent:
		return p, nil
	default:
		return Progress{}, ErrRecord
	}
}
