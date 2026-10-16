package ops

import (
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/plan"
)

// ManagedCreateCursor separates the original check boundary from one resolver-owned wake.
type ManagedCreateCursor struct {
	Boundary      StepPayload
	FrontendStart StepPayload
}

// ManagedCreateRecoveryCursor admits at most one claimed frontend wake after
// known publication. It does not admit another wake after an uncertain start.
func ManagedCreateRecoveryCursor(events []Event, p plan.Plan) (ManagedCreateCursor, bool) {
	var cursor ManagedCreateCursor
	var last StepPayload
	recoveryOutcome := ""
	if p.Kind != plan.Create || p.Lifecycle != "" || p.Managed == nil || !p.Managed.Valid() {
		return cursor, false
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
		var step StepPayload
		if ValidateEvent(event) != nil || json.Unmarshal(event.Payload, &step) != nil {
			return cursor, false
		}
		if step.Step == "managed_recovery_frontend_start" {
			if p.Managed.Frontend == nil || index != len(steps)-2 || (outcome != "intent" && outcome != "unknown") || !managedCreateOutcome(recoveryOutcome, step.Outcome) {
				return cursor, false
			}
			recoveryOutcome = step.Outcome
			cursor.FrontendStart = step
			continue
		}
		last = step
		if index >= len(steps) || last.Step != steps[index] || recoveryOutcome != "" && recoveryOutcome != "completed" {
			return cursor, false
		}
		if !managedCreateOutcome(outcome, last.Outcome) {
			return cursor, false
		}
		if last.Outcome == "unknown" && index < len(steps)-3 {
			return cursor, false
		}
		outcome = last.Outcome
		if outcome == "completed" {
			index++
			outcome = ""
		}
	}
	cursor.Boundary = last
	return cursor, last.Step == "managed_route_receipt" || last.Step == "managed_route_check" || last.Step == "managed_commit"
}

func managedCreateOutcome(before, after string) bool {
	return before == "" && after == "intent" || before == "intent" && (after == "completed" || after == "unknown") || before == "unknown" && after == "completed"
}
