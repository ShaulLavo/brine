package ops

import (
	"encoding/json"
	"reflect"
	"testing"
)

func inspectionEvent(step, outcome, code string) Event {
	payload, _ := json.Marshal(StepPayload{Step: step, Outcome: outcome, Code: code})
	return Event{Kind: "step", Payload: payload}
}
func TestInspectionRefusalProofIsExplicitPairedAndImmutable(t *testing.T) {
	original := []Event{inspectionEvent("check_direct", "failed", "health_failed"), inspectionEvent("rollback_quiesce", "intent", ""), inspectionEvent("rollback_quiesce", "failed", "effect_refused")}
	saved, _ := json.Marshal(original)
	projected, refused, valid := InspectionPrefix(original)
	if !valid || !refused || len(projected) != 1 || !reflect.DeepEqual(mustEncodeInspectionEvents(original), saved) {
		t.Fatal(projected, refused, valid, original)
	}
	again, proofAgain, validAgain := InspectionPrefix(projected)
	if !validAgain || proofAgain || !reflect.DeepEqual(again, projected) {
		t.Fatal("projection is not idempotent")
	}
	var step StepPayload
	if json.Unmarshal(projected[0].Payload, &step) != nil || step.Outcome != "intent" || step.Code != "" {
		t.Fatal(step)
	}
	for _, events := range [][]Event{
		{inspectionEvent("rollback_quiesce", "failed", "effect_refused")},
		{inspectionEvent("rollback_unit", "intent", ""), inspectionEvent("rollback_quiesce", "failed", "effect_refused")},
		{inspectionEvent("rollback_quiesce", "unknown", "interrupted"), inspectionEvent("rollback_quiesce", "failed", "effect_refused")},
	} {
		if _, _, ok := InspectionPrefix(events); ok {
			t.Fatal("unproven refusal accepted", events)
		}
	}
	ambiguous := []Event{inspectionEvent("rollback_quiesce", "intent", ""), inspectionEvent("rollback_quiesce", "failed", "rollback_failed")}
	result, proof, ok := InspectionPrefix(ambiguous)
	if !ok || proof || !reflect.DeepEqual(ambiguous, result) {
		t.Fatal("ambiguous failure inferred as refusal")
	}
	if ValidateEvent(inspectionEvent("rollback_quiesce", "unknown", "effect_refused")) == nil || ValidateEvent(inspectionEvent("rollback_quiesce", "completed", "effect_refused")) == nil || ValidateEvent(inspectionEvent("check_direct", "failed", "effect_refused")) == nil {
		t.Fatal("invalid proof accepted")
	}
}

func mustEncodeInspectionEvents(events []Event) []byte { data, _ := json.Marshal(events); return data }
