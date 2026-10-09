package reconcile

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

// RunResolution is called by run-op while holding the host mutation lock.
func (r Reconciler) RunResolution(ctx context.Context, lock ops.Lock, id string) error {
	if lock == nil {
		return errors.New("reconcile: host lock required")
	}
	op, err := r.Store.GetOperation(ctx, id)
	if err != nil {
		return err
	}
	if op.Kind != ops.Resolve || op.State.IsTerminal() {
		return result.New(result.Conflict, nil)
	}
	source, err := r.Store.GetOperation(ctx, op.RecoveryOf)
	if err != nil {
		return err
	}
	if op.PlanID == "" {
		events, e := r.events(ctx, op.ID)
		if e != nil {
			return e
		}
		if source.State != ops.RecoveryRequired || source.App != op.App || source.SecretRef != op.SecretRef || r.SecretResolution == nil {
			return result.New(result.RecoveryRequired, nil)
		}
		state, e := r.SecretResolution(ctx, op, events)
		if e != nil {
			return e
		}
		if state != ops.Succeeded && state != ops.Failed {
			return result.New(result.RecoveryRequired, nil)
		}
		if op.State == ops.Queued || op.State == ops.LaunchUnknown {
			if e = r.Store.TransitionOperation(ctx, op.ID, op.State, ops.Preflight); e != nil {
				return e
			}
			op.State = ops.Preflight
		}
		return r.Store.TransitionOperation(ctx, op.ID, op.State, state)
	}
	p, d, err := r.Store.LoadPlan(ctx, op.PlanID)
	if err != nil {
		return err
	}
	events, err := r.events(ctx, op.ID)
	if err != nil {
		return err
	}
	executor := r.Executor
	if r.ExecutorFor != nil {
		executor, err = r.ExecutorFor(ctx, op, p, d)
		if err != nil {
			return err
		}
	}
	if executor == nil {
		return result.New(result.DependencyMissing, nil)
	}
	assessment, err := executor.InspectResolution(ctx, op, source, p, d, events)
	if err != nil {
		return err
	}
	if assessment.Action == apply.RequireRecovery {
		return result.New(result.RecoveryRequired, nil)
	}
	return executor.Recover(ctx, assessment)
}
