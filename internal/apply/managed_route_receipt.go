package apply

import (
	"context"
	"github.com/ShaulLavo/brine/internal/caddy"
)

func (r *managedExecution) recordRoutes(ctx context.Context, expected uint64, before, after caddy.State) error {
	x := r.x
	return x.step(ctx, "managed_route_receipt", x.state, "interrupted", func(ctx context.Context) error {
		return x.executor.Managed.RecordRoutes(ctx, x.id, x.plan, expected, before, after)
	})
}
