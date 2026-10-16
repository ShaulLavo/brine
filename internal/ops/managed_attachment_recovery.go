package ops

import (
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/plan"
)

// InterruptedFrontendAttachment recognizes a completed installation with only
// its read-only health check unresolved. No route change has been issued.
func InterruptedFrontendAttachment(events []Event, p plan.Plan) bool {
	if p.Kind != plan.Update || p.Lifecycle != "" || p.Managed == nil || p.Managed.Frontend == nil {
		return false
	}
	for _, change := range p.Changes {
		if change.Kind == plan.StartManagedEngine || change.Kind == plan.RemoveManagedFrontend {
			return false
		}
	}
	steps := []string{"preflight", "managed_desired", "engine_stage", "frontend_pull", "frontend_verify", "frontend_secrets", "frontend_stage", "engine_check", "frontend_install", "frontend_reload", "frontend_bind", "frontend_start", "frontend_check"}
	index, outcome := 0, ""
	for _, event := range events {
		if event.Kind != "step" {
			continue
		}
		var step StepPayload
		if ValidateEvent(event) != nil || json.Unmarshal(event.Payload, &step) != nil || index >= len(steps) || step.Step != steps[index] || !managedCreateOutcome(outcome, step.Outcome) {
			return false
		}
		outcome = step.Outcome
		if outcome == "completed" {
			index++
			outcome = ""
		}
	}
	return index == len(steps)-1 && (outcome == "intent" || outcome == "unknown")
}
