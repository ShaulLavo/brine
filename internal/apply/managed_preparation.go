package apply

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

type ManagedPreparation interface {
	PrepareManaged(context.Context, string, plan.Plan, policy.Desired) error
	ManagedPrepared(context.Context, string, plan.Plan, policy.Desired) (bool, error)
}

func (e *Executor) runManagedPreparation(ctx context.Context, operation string, p plan.Plan, d policy.Desired) error {
	x := &execution{executor: e, id: operation, plan: p, desired: d}
	validate := func(ctx context.Context) error {
		if e.ManagedPreparation == nil || p.Managed == nil || !p.Managed.Valid() || p.Managed.State != data.EngineUnpublished || !desiredMatches(p, d) || p.Kind != plan.Create || len(p.Conflicts) != 0 || len(p.Changes) != 1 || p.Changes[0].Kind != plan.PrepareManagedEngine || len(p.PreparationRoots) != 1 {
			return errors.New("managed unpublished allocation required")
		}
		// The host adapter rebuilds the immutable plan with fresh root/inventory facts
		// before entering this same operation engine. Each effect rechecks ownership.
		if d.SpacetimeDB == nil || p.Managed.Database.Name != d.SpacetimeDB.Name || p.Managed.Database.Root != d.SpacetimeDB.PersistentRoot || p.Managed.Archive.Destination != d.BackupDestinations[0] {
			return errors.New("managed preparation desired input differs")
		}
		return ctx.Err()
	}
	if err := x.step(ctx, "preflight", Preflight, "drift", validate); err != nil {
		return x.fail(ctx, err)
	}
	prepare := func(ctx context.Context) error {
		effectErr := e.ManagedPreparation.PrepareManaged(ctx, operation, p, d)
		// Every attempted allocation is inspected, including ordinary errors. A
		// partial directory/configuration must never be retried or compensated away.
		inspect, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.effectTimeout())
		prepared, readErr := e.ManagedPreparation.ManagedPrepared(inspect, operation, p, d)
		cancel()
		if readErr == nil && prepared && ctx.Err() == nil {
			return nil
		}
		return &Error{Step: "prepare_managed_engine", Code: "interrupted", Cause: errors.Join(effectErr, readErr, ctx.Err())}
	}
	if err := x.step(ctx, "prepare_managed_engine", Preparing, "interrupted", prepare); err != nil {
		return x.fail(ctx, err)
	}
	return x.terminal(ctx, Succeeded, nil)
}
