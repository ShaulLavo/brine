package apply

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

// Preparation recovery only reads back an already complete allocation. A pull,
// probe, reservation, directory creation or registry write is never replayed.
func (e *Executor) inspectDataPreparationRecovery(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, events []Event, r Recovery) (Recovery, error) {
	if op.State != Preparing || p.Hash != op.PlanID || !desiredMatches(p, d) || p.Kind != plan.Create || len(p.Conflicts) != 0 || e.Facts == nil || e.PersistentData == nil {
		return r, nil
	}
	intent := false
	last := ""
	for _, event := range events {
		if err := ops.ValidateEvent(event); err != nil {
			return r, err
		}
		if event.Kind != "step" {
			continue
		}
		var step ops.StepPayload
		if err := json.Unmarshal(event.Payload, &step); err != nil {
			return r, err
		}
		if step.Step != "prepare_data" {
			return r, nil
		}
		switch step.Outcome {
		case "intent":
			if intent {
				return r, nil
			}
			intent = true
		case "unknown", "completed":
			if !intent || last == "completed" {
				return r, nil
			}
		default:
			return r, nil
		}
		last = step.Outcome
	}
	if !intent {
		return r, nil
	}
	facts, err := e.Facts.Read(ctx)
	if err != nil {
		return r, err
	}
	if !desiredMatches(p, facts.Input.Desired) || !reflect.DeepEqual(p.Target, facts.Input.Snapshot.Identity) || facts.Input.Snapshot.Generation.Status != target.KnownStatus || !reflect.DeepEqual(p.ObservedGeneration, facts.Input.Snapshot.Generation) {
		return r, nil
	}
	complete, err := e.PersistentData.PersistentPrepared(ctx, op.ID, p, facts.Input.Desired)
	if err != nil {
		return r, err
	}
	if !complete {
		return r, nil
	}
	r.Step = "prepare_data"
	r.Action = FinishSucceeded
	r.resolved = last != "completed"
	r.unknownBoundary = last == "intent"
	r.execution = &execution{executor: e, id: op.ID, plan: p, desired: d, facts: facts, state: op.State}
	return r, nil
}
