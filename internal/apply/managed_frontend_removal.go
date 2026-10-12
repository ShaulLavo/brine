package apply

import (
	"context"
	"errors"
)

type ManagedFrontendUnits interface {
	RemoveRetained(context.Context, string, string) error
	RestoreRemoved(context.Context, string, string) error
}

func (r *managedExecution) removeFrontend(ctx context.Context) error {
	x := r.x
	units, ok := x.executor.Units.(ManagedFrontendUnits)
	if !ok || !x.hasPrevious || x.previous.Managed == nil || x.previous.Managed.Frontend == nil {
		return errors.New("managed frontend removal adapters required")
	}
	if err := x.step(ctx, "frontend_remove", Starting, "unit_failed", func(ctx context.Context) error {
		err := units.RemoveRetained(ctx, x.plan.App+".container", x.previousUnitHash())
		if err == nil {
			r.frontendRemoved = true
		}
		return err
	}); err != nil {
		return err
	}
	return x.step(ctx, "frontend_remove_reload", Starting, "unit_failed", x.executor.Systemd.DaemonReload)
}
