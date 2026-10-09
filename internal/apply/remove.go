package apply

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"reflect"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type RemovalUnits interface {
	VerifyRemove(context.Context, string, string) error
	Remove(context.Context, string, string) error
}
type RemovalRoutes interface {
	Withdraw(context.Context, caddy.State, string) (caddy.Result, error)
	SettleWithdrawal(context.Context, caddy.State, caddy.State, string) error
}
type RetirementStore interface {
	RetireApp(context.Context, string, string, string) error
	AppRetired(context.Context, string, string, string) (bool, error)
}

func (e *Executor) runRemove(ctx context.Context, id string, p plan.Plan, d policy.Desired) error {
	if e.Facts == nil || e.Releases == nil || !desiredMatches(p, d) || len(p.Conflicts) != 0 || p.Kind != plan.Update && p.Kind != plan.NoOp {
		return result.New(result.Conflict, nil)
	}
	facts, err := e.Facts.Read(ctx)
	if err != nil {
		return err
	}
	fresh, err := plan.BuildRemove(facts.Input)
	if err != nil {
		return err
	}
	original, err := p.CanonicalBytes()
	if err != nil {
		return err
	}
	rebuilt, err := fresh.CanonicalBytes()
	if err != nil || !bytes.Equal(original, rebuilt) {
		return result.New(result.Conflict, err)
	}
	x := &execution{executor: e, id: id, plan: p, desired: d, facts: facts}
	if err = x.step(ctx, "preflight", Preflight, "drift", func(ctx context.Context) error {
		if p.Kind == plan.NoOp {
			return nil
		}
		if p.Removal == nil || len(p.Removal.Units) != 1 {
			return errors.New("apply: missing removal ownership")
		}
		units, ok := e.Units.(RemovalUnits)
		if !ok {
			return errors.New("apply: removal unit adapter required")
		}
		if _, ok = e.Routes.(RemovalRoutes); !ok {
			return errors.New("apply: removal route adapter required")
		}
		if _, ok = e.Releases.(RetirementStore); !ok || e.Systemd == nil || e.Podman == nil {
			return errors.New("apply: removal runtime adapters required")
		}
		x.previous, x.hasPrevious, err = e.Releases.CurrentRelease(ctx, p.App)
		if err != nil {
			return err
		}
		if !x.hasPrevious || x.previous.ID != p.Removal.ReleaseID || !reflect.DeepEqual(x.previous.Units, p.Removal.Units) || x.previous.CaddyFile != p.Removal.Route {
			return errors.New("apply: removal release drift")
		}
		x.service, err = systemd.ParseUnit(p.App + ".service")
		if err != nil {
			return err
		}
		return units.VerifyRemove(ctx, p.Removal.Units[0].Name, p.Removal.Units[0].Hash)
	}); err != nil {
		return x.terminal(ctx, Failed, err)
	}
	if p.Kind == plan.NoOp {
		return x.terminal(ctx, Succeeded, nil)
	}
	return x.remove(ctx, nil, false)
}

func removalRouting(p plan.Plan, facts Facts) caddy.State {
	before := caddy.State{Generation: p.Removal.Routing.Generation, Files: map[string]string{}, Sites: maps.Clone(facts.Routing.Sites)}
	for _, file := range p.Removal.Routing.Files {
		before.Files[file.Name] = file.Hash
	}
	return before
}

func (x *execution) remove(ctx context.Context, completed map[string]bool, settleRoute bool) error {
	e := x.executor
	units := e.Units.(RemovalUnits)
	routes := e.Routes.(RemovalRoutes)
	store := e.Releases.(RetirementStore)
	before := removalRouting(x.plan, x.facts)
	steps := []struct {
		name   string
		state  State
		code   string
		effect func(context.Context) error
	}{
		{"withdraw_route", Preparing, "route_invalid", func(ctx context.Context) error {
			if settleRoute {
				return routes.SettleWithdrawal(ctx, before, x.facts.Routing, x.plan.App)
			}
			result, err := routes.Withdraw(ctx, before, x.plan.App)
			if result.Outcome == caddy.Unknown {
				return &Error{Step: "withdraw_route", Code: "reload_unknown", Cause: err}
			}
			if result.Outcome == caddy.RecoveryRequired {
				return &Error{Step: "withdraw_route", Code: "interrupted", Cause: err}
			}
			if err != nil {
				return err
			}
			if result.Outcome != caddy.Applied {
				return errors.New("apply: route not withdrawn")
			}
			x.route = result.Next
			return nil
		}},
		{"stop_unit", Quiescing, "stop_failed", func(ctx context.Context) error {
			if x.waitWriter(ctx) == writerStopped {
				return nil
			}
			if err := x.stop(ctx); err != nil {
				return err
			}
			if x.waitWriter(ctx) != writerStopped {
				return &Error{Step: "stop_unit", Code: "interrupted"}
			}
			return nil
		}},
		{"remove_unit", Starting, "unit_failed", func(ctx context.Context) error {
			if x.waitWriter(ctx) != writerStopped {
				return &Error{Step: "remove_unit", Code: "interrupted"}
			}
			unit := x.plan.Removal.Units[0]
			return units.Remove(ctx, unit.Name, unit.Hash)
		}},
		{"reload_units", Checking, "unit_failed", e.Systemd.DaemonReload},
		{"retire_app", Committing, "commit_failed", func(ctx context.Context) error {
			if x.waitWriter(ctx) != writerStopped {
				return &Error{Step: "retire_app", Code: "interrupted"}
			}
			return store.RetireApp(ctx, x.id, x.plan.App, x.plan.Removal.ReleaseID)
		}},
	}
	for _, step := range steps {
		if completed[step.name] {
			continue
		}
		if err := x.step(ctx, step.name, step.state, step.code, step.effect); err != nil {
			return x.terminal(ctx, RecoveryRequired, err)
		}
	}
	return x.terminal(ctx, Succeeded, nil)
}

func removalRouteState(p plan.Plan, facts Facts) resolution {
	cfg := facts.Input.Snapshot.CaddyConfig
	if cfg.Status != target.KnownStatus || cfg.Value == nil {
		return unresolved
	}
	before := map[string]string{}
	for _, file := range p.Removal.Routing.Files {
		before[file.Name] = file.Hash
	}
	actual := map[string]string{}
	for _, file := range cfg.Value.Files {
		actual[file.Name] = file.Hash
	}
	if cfg.Value.Generation == p.Removal.Routing.Generation && maps.Equal(before, actual) {
		return notApplied
	}
	delete(before, p.App+".caddy")
	if cfg.Value.Generation > p.Removal.Routing.Generation && maps.Equal(before, actual) {
		return applied
	}
	return unresolved
}
