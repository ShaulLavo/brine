package apply

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/plan"
)

func (r *managedExecution) setDesired(ctx context.Context, name string, state State, before uint64, flags plan.ManagedLifecyclePlan) error {
	x := r.x
	return x.step(ctx, name, state, "interrupted", func(ctx context.Context) error {
		err := x.executor.Managed.SetDesired(ctx, x.id, x.plan, before, flags)
		inspect, cancel := context.WithTimeout(context.WithoutCancel(ctx), x.executor.effectTimeout())
		defer cancel()
		applied, readErr := x.executor.Managed.DesiredMatches(inspect, x.id, x.plan, before+1, flags)
		if applied && readErr == nil && ctx.Err() == nil {
			return nil
		}
		return &Error{Step: name, Code: "interrupted", Cause: errors.Join(err, readErr, ctx.Err())}
	})
}
