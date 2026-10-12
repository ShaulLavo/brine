package apply

import (
	"context"
	"errors"
	"time"
)

func (r *managedExecution) compensate(ctx context.Context, cause error) error {
	x := r.x
	e := x.executor
	var failure *Error
	if errors.As(cause, &failure) && (failure.Code == "interrupted" || failure.Code == "reload_unknown" || failure.Code == "journal_failed" || failure.Code == "rollback_failed") {
		return x.terminal(ctx, RecoveryRequired, cause)
	}
	if !x.installed && !x.quiesced && !x.published && !r.frontendBound {
		return x.terminal(ctx, Failed, cause)
	}
	rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*e.effectTimeout()+3*time.Duration(x.previousDesired.Health.StartupDeadlineSeconds)*time.Second)
	defer cancel()
	// Every restored client artifact uses the current native runtime. Historical
	// module/data/key/epoch references are never applied from a frontend release.
	if e.Managed.EngineReady(rollback, x.plan) != nil {
		return x.terminal(ctx, RecoveryRequired, cause)
	}
	if r.hadRelease && (r.previousPlan.Managed == nil || r.previousPlan.Managed.RuntimeSHA256 != x.plan.Managed.RuntimeSHA256 || r.previousPlan.Managed.Archive.EpochID != x.plan.Managed.Archive.EpochID) {
		return x.terminal(ctx, RecoveryRequired, cause)
	}
	step := func(name string, effect func(context.Context) error) error {
		return x.step(rollback, name, RollingBack, "rollback_failed", effect)
	}
	if x.started {
		if err := step("rollback_quiesce", x.stop); err != nil {
			return x.terminal(ctx, RecoveryRequired, errors.Join(cause, err))
		}
	}
	if x.installed {
		if err := step("rollback_unit", func(ctx context.Context) error {
			if e.Managed.EngineReady(ctx, x.plan) != nil {
				return errors.New("managed current engine changed")
			}
			return e.Units.Rollback(ctx, x.unit.Name(), x.unit.Hash(), x.previousUnitHash())
		}); err != nil {
			return x.terminal(ctx, RecoveryRequired, errors.Join(cause, err))
		}
		if err := step("rollback_reload", e.Systemd.DaemonReload); err != nil {
			return x.terminal(ctx, RecoveryRequired, errors.Join(cause, err))
		}
	}
	if r.frontendBound {
		if err := step("managed_frontend_restore_permit", func(ctx context.Context) error {
			if e.Managed.EngineReady(ctx, x.plan) != nil {
				return errors.New("managed engine changed before permit restoration")
			}
			return e.Managed.RestoreFrontendPermit(ctx, x.id, r.previousFrontendPermit)
		}); err != nil {
			return x.terminal(ctx, RecoveryRequired, errors.Join(cause, err))
		}
	}
	if x.hasPrevious {
		if err := step("rollback_start", func(ctx context.Context) error {
			if e.Managed.EngineReady(ctx, x.plan) != nil {
				return errors.New("current engine not ready")
			}
			return e.Systemd.Start(ctx, x.service)
		}); err != nil {
			return x.terminal(ctx, RecoveryRequired, errors.Join(cause, err))
		}
		if err := step("rollback_check", func(ctx context.Context) error { return x.check(ctx, x.previousDesired, x.previous.HostPort, false) }); err != nil {
			return x.terminal(ctx, RecoveryRequired, errors.Join(cause, err))
		}
	}
	if x.published {
		if err := step("rollback_route", func(ctx context.Context) error {
			if e.Managed.EngineReady(ctx, x.plan) != nil {
				return errors.New("managed route restoration fence changed")
			}
			return e.Routes.Restore(ctx, x.route, x.facts.Routing)
		}); err != nil {
			return x.terminal(ctx, RecoveryRequired, errors.Join(cause, err))
		}
	}
	return x.terminal(ctx, RolledBack, cause)
}
