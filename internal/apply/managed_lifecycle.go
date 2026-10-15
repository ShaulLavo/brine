package apply

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

func (e *Executor) runManagedLifecycle(ctx context.Context, id string, p plan.Plan, d policy.Desired) error {
	r := &managedExecution{x: &execution{executor: e, id: id, plan: p, desired: d}}
	x := r.x
	if p.ManagedLifecycle == nil || p.Lifecycle != plan.StartApp && p.Lifecycle != plan.StopApp && p.Lifecycle != plan.RestartApp {
		return errors.New("typed managed lifecycle required")
	}
	if err := x.step(ctx, "preflight", Preflight, "drift", r.preflight); err != nil {
		return x.lifecycleFailure(ctx, err)
	}
	revision := p.ManagedIntentRevision
	if p.Lifecycle != plan.StartApp {
		paused := *p.ManagedLifecycle
		paused.FrontendRunning, paused.IngressServing = false, false
		if paused.Engine {
			paused.EngineRunning = false
		}
		if err := r.setDesired(ctx, "managed_pause", Preparing, revision, paused); err != nil {
			return x.lifecycleFailure(ctx, err)
		}
		revision++
		if err := r.withdrawManagedRoutes(ctx); err != nil {
			return x.lifecycleFailure(ctx, err)
		}
		if err := r.stopManagedComponent(ctx); err != nil {
			return x.lifecycleFailure(ctx, err)
		}
		if err := r.recordRoutes(ctx, p.ManagedRouteRevision, x.facts.Routing, x.route); err != nil {
			return x.lifecycleFailure(ctx, err)
		}
		if p.Lifecycle == plan.StopApp {
			return x.terminal(ctx, Succeeded, nil)
		}
	}
	resumeState := Starting
	if p.Lifecycle == plan.StartApp {
		resumeState = Preparing
	}
	if err := r.setDesired(ctx, "managed_resume", resumeState, revision, *p.ManagedLifecycle); err != nil {
		return x.lifecycleFailure(ctx, err)
	}
	if err := r.startManagedComponent(ctx); err != nil {
		return x.lifecycleFailure(ctx, err)
	}
	withdrawn := x.route
	if err := r.publishManagedRoutes(ctx); err != nil {
		return x.lifecycleFailure(ctx, err)
	}
	routeRevision := p.ManagedRouteRevision
	before := x.facts.Routing
	if p.Lifecycle == plan.RestartApp {
		routeRevision++
		before = withdrawn
	}
	if err := r.recordRoutes(ctx, routeRevision, before, x.route); err != nil {
		return x.lifecycleFailure(ctx, err)
	}
	return x.terminal(ctx, Succeeded, nil)
}

func (r *managedExecution) withdrawManagedRoutes(ctx context.Context) error {
	x := r.x
	routes, ok := x.executor.Routes.(RemovalRoutes)
	if !ok {
		return errors.New("managed route withdrawal required")
	}
	return x.step(ctx, "managed_withdraw", Quiescing, "route_invalid", func(ctx context.Context) error {
		// Planning facts omit the site only after a recorded protected withdrawal.
		if _, present := x.facts.Routing.Files[x.plan.App+".caddy"]; !present {
			x.route = x.facts.Routing
			return nil
		}
		result, err := routes.Withdraw(ctx, x.facts.Routing, x.plan.App)
		x.route = result.Next
		if result.Outcome == caddy.Unknown {
			return &Error{Step: "managed_withdraw", Code: "reload_unknown", Cause: err}
		}
		if err != nil || result.Outcome != caddy.Applied {
			return errors.Join(err, errors.New("managed route not withdrawn"))
		}
		return nil
	})
}
func (r *managedExecution) stopManagedComponent(ctx context.Context) error {
	x := r.x
	if x.plan.ManagedLifecycle.Frontend {
		return x.step(ctx, "managed_frontend_stop", Quiescing, "stop_failed", func(ctx context.Context) error {
			if err := x.stop(ctx); err != nil {
				return err
			}
			if x.waitWriter(ctx) != writerStopped {
				return &Error{Step: "managed_frontend_stop", Code: "interrupted"}
			}
			return nil
		})
	}
	return x.step(ctx, "managed_engine_stop", Quiescing, "stop_failed", func(ctx context.Context) error {
		if x.executor.Managed.EngineStopped(ctx, x.plan) == nil {
			return nil
		}
		if err := x.executor.Managed.BeforeEngineStop(ctx, x.plan); err != nil {
			return err
		}
		err := x.executor.Systemd.Stop(ctx, r.engineService)
		if err != nil {
			return err
		}
		if err := x.executor.Managed.EngineStopped(ctx, x.plan); err != nil {
			return &Error{Step: "managed_engine_stop", Code: "interrupted", Cause: err}
		}
		return nil
	})
}
func (r *managedExecution) startManagedComponent(ctx context.Context) error {
	x := r.x
	if x.plan.ManagedLifecycle.Engine {
		if err := r.startLifecycleEngine(ctx); err != nil {
			return err
		}
	}
	if !x.plan.ManagedLifecycle.Frontend {
		return nil
	}
	if err := x.step(ctx, "managed_frontend_start", Starting, "start_failed", func(ctx context.Context) error {
		if err := x.executor.Managed.BeforeFrontendStart(ctx, x.id, x.plan, x.desired, x.previousUnitHash()); err != nil {
			return err
		}
		return x.executor.Systemd.Start(ctx, x.service)
	}); err != nil {
		return err
	}
	return x.step(ctx, "managed_frontend_check", Checking, "health_failed", func(ctx context.Context) error { return x.check(ctx, x.desired, x.plan.HostPort, false) })
}

func (r *managedExecution) startLifecycleEngine(ctx context.Context) error {
	x := r.x
	if err := x.step(ctx, "managed_engine_start", Starting, "start_failed", func(ctx context.Context) error {
		if x.executor.Managed.EngineReady(ctx, x.plan) == nil {
			return nil
		}
		if err := x.executor.Managed.BeforeEngineStart(ctx, x.plan); err != nil {
			return err
		}
		return x.executor.Systemd.Start(ctx, r.engineService)
	}); err != nil {
		return err
	}
	checkState := Checking
	if x.plan.ManagedLifecycle.Frontend {
		checkState = Starting
	}
	return x.step(ctx, "managed_engine_check", checkState, "health_failed", func(ctx context.Context) error { return x.executor.Managed.EngineReady(ctx, x.plan) })
}
func (r *managedExecution) publishManagedRoutes(ctx context.Context) error {
	x := r.x
	before := x.facts.Routing
	if x.plan.Lifecycle == plan.RestartApp {
		before = x.route
	}
	return x.step(ctx, "managed_route", Checking, "route_invalid", func(ctx context.Context) error {
		if err := x.executor.Managed.BeforeIngress(ctx, x.id, x.plan, x.desired); err != nil {
			return err
		}
		result, err := x.executor.Routes.Publish(ctx, before, x.plan, x.desired)
		x.route = result.Next
		if result.Outcome == caddy.Unknown {
			return &Error{Step: "managed_route", Code: "reload_unknown", Cause: err}
		}
		if err != nil || result.Outcome != caddy.Applied {
			return errors.Join(err, errors.New("managed route not published"))
		}
		return nil
	})
}
