package apply

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/plan"
)

func (x *execution) lifecycle(ctx context.Context) error {
	stateCtx, cancel := context.WithTimeout(ctx, journalTimeout)
	err := x.executor.Journal.SetOperationState(stateCtx, x.id, Preparing)
	cancel()
	if err != nil {
		return x.terminal(ctx, RecoveryRequired, &Error{Step: "preflight", Code: "journal_failed", Cause: err})
	}
	x.state = Preparing
	if x.plan.Lifecycle == plan.StopApp || x.plan.Lifecycle == plan.RestartApp {
		err = x.step(ctx, "stop_unit", Quiescing, "stop_failed", func(ctx context.Context) error {
			if x.inspectWriter(ctx) == writerStopped {
				return nil
			}
			if err := x.stop(ctx); err != nil {
				return err
			}
			// A successful stop call is not a fence for a still-pending manager job.
			if x.waitWriter(ctx) != writerStopped {
				return &Error{Step: "stop_unit", Code: "interrupted"}
			}
			return nil
		})
		if err != nil {
			return x.lifecycleFailure(ctx, err)
		}
		if x.plan.Lifecycle == plan.StopApp {
			return x.terminal(ctx, Succeeded, nil)
		}
	}
	if err = x.step(ctx, "reload_units", Starting, "unit_failed", x.executor.Systemd.DaemonReload); err != nil {
		return x.lifecycleFailure(ctx, err)
	}
	if err = x.step(ctx, "start_unit", Starting, "start_failed", func(ctx context.Context) error {
		if x.inspectWriter(ctx) == writerRunning {
			return nil
		}
		return x.executor.Systemd.Start(ctx, x.service)
	}); err != nil {
		return x.lifecycleFailure(ctx, err)
	}
	if err = x.step(ctx, "check_direct", Checking, "health_failed", func(ctx context.Context) error { return x.check(ctx, x.desired, x.plan.HostPort, false) }); err != nil {
		return x.lifecycleFailure(ctx, err)
	}
	// Lifecycle changes do not create releases, replace units or touch routes.
	return x.terminal(ctx, Succeeded, nil)
}
func (x *execution) lifecycleFailure(ctx context.Context, cause error) error {
	var failure *Error
	if errors.As(cause, &failure) && (failure.Code == "journal_failed" || failure.Code == "interrupted" || failure.Code == "reload_unknown") {
		return x.terminal(ctx, RecoveryRequired, cause)
	}
	return x.terminal(ctx, Failed, cause)
}
