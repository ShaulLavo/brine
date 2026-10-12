package apply

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

// Prepared recovery proves only complete prior effects. It never recreates a
// tree, config, reservation, unit, service, route, or module after interruption.
func (e *Executor) inspectManagedRecovery(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, events []Event, r Recovery) (Recovery, error) {
	if p.Managed == nil || !p.Managed.Valid() || p.Hash != op.PlanID || !desiredMatches(p, d) || len(p.Conflicts) != 0 || op.State != Preparing || p.Managed.State != data.EngineUnpublished || e.ManagedPreparation == nil {
		return r, nil
	}
	index := 0
	steps := []string{"preflight", "prepare_managed_engine"}
	lastOutcome := ""
	for _, event := range events {
		if err := ops.ValidateEvent(event); err != nil {
			return r, err
		}
		if event.Kind != "step" {
			continue
		}
		var step ops.StepPayload
		if json.Unmarshal(event.Payload, &step) != nil || index >= len(steps) || step.Step != steps[index] {
			return r, nil
		}
		switch step.Outcome {
		case "intent":
			if lastOutcome != "" {
				return r, nil
			}
			lastOutcome = "intent"
		case "unknown":
			if lastOutcome != "intent" {
				return r, nil
			}
			lastOutcome = "unknown"
		case "completed":
			if lastOutcome != "intent" && lastOutcome != "unknown" {
				return r, nil
			}
			index++
			lastOutcome = ""
		default:
			return r, nil
		}
		r.Step = step.Step
	}
	if r.Step != "prepare_managed_engine" || index < 1 {
		return r, nil
	}
	source := op.ID
	if len(r.resolutionOwners) > 0 {
		source = r.resolutionOwners[len(r.resolutionOwners)-1]
	}
	inspect, cancel := context.WithTimeout(ctx, e.effectTimeout())
	defer cancel()
	complete, err := e.ManagedPreparation.ManagedPrepared(inspect, source, p, d)
	if err != nil {
		return r, err
	}
	if !complete || inspect.Err() != nil {
		return r, nil
	}
	r.Action = FinishSucceeded
	r.resolved = index != 2
	r.unknownBoundary = lastOutcome == "intent"
	r.execution = &execution{executor: e, id: op.ID, plan: p, desired: d, state: op.State}
	return r, nil
}

func managedRecoveryState(step string) State {
	states := map[string]State{"preflight": Preflight, "prepare_managed_engine": Preparing, "engine_stage": Preparing, "frontend_pull": Preparing, "frontend_verify": Preparing, "frontend_secrets": Preparing, "frontend_stage": Preparing, "frontend_stop": Quiescing, "engine_install": Starting, "engine_reload": Starting, "engine_start": Starting, "engine_check": Starting, "frontend_install": Starting, "frontend_reload": Starting, "frontend_bind": Starting, "frontend_start": Starting, "frontend_check": Checking, "managed_route": Checking, "managed_route_check": Checking, "managed_commit": Committing}
	return states[step]
}
