package apply

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

// Resolution preserves the failed receipt. Its successor owns every new effect.
// The source prefix is inspected again under the host lock, not replanned as drift.
func (e *Executor) InspectResolution(ctx context.Context, successor, source Operation, p plan.Plan, d policy.Desired, events []Event) (Recovery, error) {
	refused := Recovery{Action: RequireRecovery}
	if successor.Kind != ops.Resolve || successor.RecoveryOf != source.ID || source.State != RecoveryRequired || successor.PlanID != source.PlanID || source.Kind != ops.Deploy && source.Kind != ops.Resolve {
		return refused, nil
	}
	owners, err := e.resolutionSourceOwners(ctx, successor, source)
	if err != nil || len(owners) == 0 {
		return refused, err
	}
	projected := successor
	projected.State = Preflight
	for _, event := range events {
		if event.Kind != "step" {
			continue
		}
		var step ops.StepPayload
		if json.Unmarshal(event.Payload, &step) != nil {
			return refused, nil
		}
		if p.Lifecycle == plan.RemoveApp {
			projected.State = map[string]State{"preflight": Preflight, "withdraw_route": Preparing, "stop_unit": Quiescing, "remove_unit": Starting, "reload_units": Checking, "retire_app": Committing}[step.Step]
		} else {
			projected.State = map[string]State{"preflight": Preflight, "pull_image": Preparing, "verify_image": Preparing, "ensure_secrets": Preparing, "stage_unit": Preparing, "quiesce_old": Quiescing, "install_unit": Starting, "reload_units": Starting, "start_unit": Starting, "check_direct": Checking, "publish_route": Checking, "check_routed": Checking, "commit": Committing}[step.Step]
		}
		if projected.State == "" {
			return refused, nil
		}
	}
	r, err := e.inspectRecovery(ctx, projected, p, d, events, owners)
	if err != nil || r.Action == RequireRecovery {
		return r, err
	}
	r.operation = successor
	r.execution.id = successor.ID
	r.execution.state = successor.State
	r.resolutionState = projected.State
	return r, nil
}

func (x *execution) prepareResolution(ctx context.Context, destination State) error {
	if destination == "" {
		return nil
	}
	path := []State{Preflight, Preparing, Quiescing, Starting, Checking, Committing}
	destinationIndex := slices.Index(path, destination)
	if destinationIndex < 0 {
		return errors.New("apply: invalid resolution phase")
	}
	// The durable transition can precede its step intent. Preserve a phase
	// already reached; never walk past the phase justified by the step prefix.
	if slices.Index(path, x.state) >= destinationIndex {
		return nil
	}
	if x.state == LaunchUnknown {
		x.state = Queued
	}
	for _, state := range path[:destinationIndex+1] {
		if x.state == destination {
			return nil
		}
		if !ops.CanTransitionFor(ops.Resolve, x.state, state) {
			continue
		}
		if err := x.executor.Journal.SetOperationState(ctx, x.id, state); err != nil {
			return err
		}
		x.state = state
	}
	if x.state != destination {
		return errors.New("apply: invalid resolution phase")
	}
	return nil
}

// Every new effect belongs to a receipt in this immutable chain. A resolution
// that only settled an ancestor can itself fail to journal its terminal state;
// that must not make the ancestor commit/retirement proof unreachable.
func (e *Executor) resolutionSourceOwners(ctx context.Context, successor, source Operation) ([]string, error) {
	probe, cancel := context.WithTimeout(ctx, e.effectTimeout())
	defer cancel()
	owners := []string{}
	seen := map[string]bool{successor.ID: true}
	app, ref := source.App, source.SecretRef
	for depth := 0; depth < 64; depth++ {
		if seen[source.ID] || source.State != RecoveryRequired || source.PlanID != successor.PlanID || source.App != app || source.SecretRef != ref {
			return nil, nil
		}
		seen[source.ID] = true
		owners = append(owners, source.ID)
		if source.Kind == ops.Deploy {
			return owners, nil
		}
		if source.Kind != ops.Resolve || e.Journal == nil {
			return nil, nil
		}
		parentID := source.RecoveryOf
		parent, err := e.Journal.GetOperation(probe, parentID)
		if err != nil {
			return nil, err
		}
		if parent.ID != parentID {
			return nil, nil
		}
		source = parent
	}
	return nil, nil
}
