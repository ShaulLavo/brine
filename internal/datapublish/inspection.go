package datapublish

import (
	"encoding/json"
)

// Inspection exposes retained references and progress, never host paths,
// private native inputs or proof authority supplied by an operator.
type Inspection struct {
	PlanID          string      `json:"plan_id"`
	App             string      `json:"app"`
	OperationID     string      `json:"operation_id"`
	PointID         string      `json:"point_id"`
	State           CursorState `json:"state"`
	Step            Step        `json:"step"`
	TransitionState string      `json:"transition_state"`
	NeedsInspection bool        `json:"needs_inspection"`
}

func (v Inspection) Valid() bool {
	if !planID(v.PlanID) || !appName.MatchString(v.App) || !identity(v.PointID) {
		return false
	}
	if v.OperationID == "" {
		return v.State == "reviewed" && v.Step == "" && v.TransitionState == "" && !v.NeedsInspection
	}
	if !operationReference.MatchString(v.OperationID) {
		return false
	}
	if v.TransitionState != "" && v.State != Finished && v.State != SourcePreserved && v.Step != FinalTransition {
		return false
	}
	switch v.TransitionState {
	case "", "prepared", "preserve_intent", "original_preserved", "promote_intent", "target_promoted", "activation_intent", "activated", "finish_intent", "completed", "abandon_intent", "abandoned_current", "not_admitted":
	default:
		return false
	}
	switch v.State {
	case Ready, IntentRecorded, InspectRequired, Finished, AttemptFailed, SourcePreserved:
	default:
		return false
	}
	if v.State == Finished {
		return v.Step == "" && v.TransitionState == "completed" && !v.NeedsInspection
	}
	if v.State == SourcePreserved {
		return v.Step == "" && v.TransitionState == "not_admitted" && !v.NeedsInspection
	}
	if v.NeedsInspection != (v.State == InspectRequired || v.State == IntentRecorded) {
		return false
	}
	switch v.Step {
	case RestoreStage, CheckPreflight, PublishStage, VerifyStage, DisposeStage, FinalTransition:
		return true
	default:
		return false
	}
}

func DecodeInspection(raw []byte) (Inspection, error) {
	var view Inspection
	fields := []string{"plan_id", "app", "operation_id", "point_id", "state", "step", "transition_state", "needs_inspection"}
	if len(raw) > 4096 {
		return view, ErrRecord
	}
	if _, err := objectValues(raw, fields...); err != nil || json.Unmarshal(raw, &view) != nil || !view.Valid() {
		return Inspection{}, ErrRecord
	}
	return view, nil
}
