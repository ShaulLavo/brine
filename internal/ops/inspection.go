package ops

import "encoding/json"

// InspectionPrefix projects only durable proof that an intended effect was not
// attempted. It never edits receipts or infers this proof from rollback_failed.
// Results are the projected prefix, whether a proven refusal was removed, and
// whether the projection was valid.
func InspectionPrefix(events []Event) ([]Event, bool, bool) {
	prefix := make([]Event, 0, len(events))
	refused := false
	for _, event := range events {
		var step StepPayload
		if event.Kind == "step" && (ValidateEvent(event) != nil || json.Unmarshal(event.Payload, &step) != nil) {
			return nil, false, false
		}
		if event.Kind == "step" && step.Code == "effect_refused" {
			intent := -1
			for i := len(prefix) - 1; i >= 0; i-- {
				if prefix[i].Kind == "step" {
					intent = i
					break
				}
			}
			if intent < 0 {
				return nil, false, false
			}
			var before StepPayload
			if ValidateEvent(prefix[intent]) != nil || json.Unmarshal(prefix[intent].Payload, &before) != nil || before.Step != step.Step || before.Outcome != "intent" {
				return nil, false, false
			}
			prefix = append(prefix[:intent], prefix[intent+1:]...)
			refused = true
			continue
		}
		prefix = append(prefix, event)
	}
	// A failed health probe may have triggered the refused rollback effect.
	// It was read-only, not an unknown mutation; inspect its forward boundary anew.
	if refused {
		for i := len(prefix) - 1; i >= 0; i-- {
			if prefix[i].Kind != "step" {
				continue
			}
			var step StepPayload
			if json.Unmarshal(prefix[i].Payload, &step) != nil {
				return nil, false, false
			}
			if step.Outcome == "failed" && (step.Step == "check_direct" || step.Step == "check_routed") && (step.Code == "health_failed" || step.Code == "health_timeout") {
				step.Outcome, step.Code = "intent", ""
				prefix[i].Payload, _ = json.Marshal(step)
			}
		}
	}
	return prefix, refused, true
}
