package apply

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

func (e *Executor) inspectManagedLifecycle(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, events []Event, r Recovery) (Recovery, error) {
	if p.ManagedLifecycle == nil || !p.ManagedLifecycle.Valid(p.Lifecycle, *p.Managed, p.ManagedIntentRevision) {
		return r, nil
	}
	last, valid := managedLifecycleRecoveryPrefix(events, p)
	r.Step = last.Step
	if !valid {
		return r, nil
	}
	routes, ok := e.Routes.(ManagedRecoveryRoutes)
	if !ok {
		return r, nil
	}
	inspect, cancel := context.WithTimeout(ctx, e.effectTimeout())
	defer cancel()
	facts, err := e.Facts.Read(inspect)
	if err != nil {
		return r, err
	}
	owners := append([]string{op.ID}, r.resolutionOwners...)
	proved := false
	for _, owner := range owners {
		if e.Managed.CompletedManaged(inspect, owner, p, d) == nil {
			proved = true
			break
		}
	}
	if !proved || routes.InspectManagedLoaded(inspect, facts.Routing, p, d) != nil || inspect.Err() != nil {
		return r, nil
	}
	if p.ManagedLifecycle.FrontendRunning && !managedRecoveryFrontendImage(facts, p) {
		return r, nil
	}
	r.Action = FinishSucceeded
	r.resolutionState = managedRecoveryState(last.Step)
	r.resolved, r.unknownBoundary = last.Outcome != "completed", last.Outcome == "intent"
	r.execution = &execution{executor: e, id: op.ID, plan: p, desired: d, state: op.State}
	return r, nil
}

func managedLifecycleRecoveryPrefix(events []Event, p plan.Plan) (ops.StepPayload, bool) {
	steps := []string{"preflight"}
	if p.Lifecycle != plan.StartApp {
		component := "managed_engine_stop"
		if p.ManagedLifecycle.Frontend {
			component = "managed_frontend_stop"
		}
		steps = append(steps, "managed_pause", "managed_withdraw", component, "managed_route_receipt")
	}
	if p.Lifecycle != plan.StopApp {
		steps = append(steps, "managed_resume")
		if p.ManagedLifecycle.Engine {
			steps = append(steps, "managed_engine_start", "managed_engine_check")
		}
		if p.ManagedLifecycle.Frontend {
			steps = append(steps, "managed_frontend_start", "managed_frontend_check")
		}
		steps = append(steps, "managed_route", "managed_route_receipt")
	}
	var last ops.StepPayload
	index, outcome := 0, ""
	for _, event := range events {
		if event.Kind != "step" {
			continue
		}
		if ops.ValidateEvent(event) != nil || json.Unmarshal(event.Payload, &last) != nil || index >= len(steps) || last.Step != steps[index] || !managedOutcomeFollows(outcome, last.Outcome) {
			return last, false
		}
		outcome = last.Outcome
		if outcome == "completed" && index < len(steps)-1 {
			index++
			outcome = ""
		}
	}
	return last, index == len(steps)-1 && slices.Contains([]string{"intent", "unknown", "completed"}, outcome)
}
