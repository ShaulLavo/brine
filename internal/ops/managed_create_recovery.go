package ops

import (
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/plan"
)

// ManagedCreateRecoveryPrefix accepts only the exact applied create path ending
// at its read-only route check or release commit. Earlier uncertain effects and
// compensation cannot authorize adopting installed artifacts.
func ManagedCreateRecoveryPrefix(events []Event, p plan.Plan) (StepPayload, bool) {
	var last StepPayload
	if p.Kind != plan.Create || p.Lifecycle != "" || p.Managed == nil || !p.Managed.Valid() {
		return last, false
	}
	steps := []string{"preflight", "managed_desired", "engine_stage"}
	if p.Managed.Frontend != nil {
		steps = append(steps, "frontend_pull", "frontend_verify", "frontend_secrets", "frontend_stage")
	}
	for _, change := range p.Changes {
		if change.Kind == plan.StartManagedEngine {
			steps = append(steps, "engine_install", "engine_reload", "engine_start")
		}
	}
	steps = append(steps, "engine_check")
	if p.Managed.Frontend != nil {
		steps = append(steps, "frontend_install", "frontend_reload", "frontend_bind", "frontend_start", "frontend_check")
	}
	steps = append(steps, "managed_route", "managed_route_receipt", "managed_route_check", "managed_commit")
	index, outcome := 0, ""
	for _, event := range events {
		if event.Kind != "step" {
			continue
		}
		if ValidateEvent(event) != nil || json.Unmarshal(event.Payload, &last) != nil || index >= len(steps) || last.Step != steps[index] {
			return last, false
		}
		if !(outcome == "" && last.Outcome == "intent" || outcome == "intent" && (last.Outcome == "completed" || last.Outcome == "unknown") || outcome == "unknown" && last.Outcome == "completed") {
			return last, false
		}
		if last.Outcome == "unknown" && index < len(steps)-2 {
			return last, false
		}
		outcome = last.Outcome
		if outcome == "completed" {
			index++
			outcome = ""
		}
	}
	return last, last.Step == "managed_route_check" || last.Step == "managed_commit"
}
