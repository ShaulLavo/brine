package apply

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

// ManagedResolutionReleases commits the original owner's proven artifacts without rebinding
// live permits or issuing another component/route mutation.
type ManagedResolutionReleases interface {
	CommitManagedResolutionRelease(context.Context, string, uint64, Release) error
}

func (e *Executor) inspectManagedRoute(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, last ops.StepPayload, r Recovery) (Recovery, error) {
	r.Step = last.Step
	owner := op.ID
	if len(r.resolutionOwners) > 0 {
		owner = r.resolutionOwners[len(r.resolutionOwners)-1]
	}
	if owner != op.ID {
		if _, ok := e.Releases.(ManagedResolutionReleases); !ok {
			return r, nil
		}
	}
	x := &execution{executor: e, id: op.ID, plan: p, desired: d, state: op.State, managedRouteOwner: owner}
	if p.Managed.Frontend != nil {
		if e.Health == nil || e.Systemd == nil || e.Podman == nil || p.Image.ManifestDigest.Status != target.KnownStatus || p.Image.ManifestDigest.Value == nil {
			return r, nil
		}
		var err error
		x.unit, err = quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
		if err != nil {
			return r, nil
		}
		x.service, err = systemd.ParseUnit(p.App + ".service")
		if err != nil {
			return r, nil
		}
	}
	inspect, cancel := context.WithTimeout(ctx, e.effectTimeout())
	defer cancel()
	if err := x.proveManagedRoute(inspect); err != nil {
		return r, nil
	}
	r.Action, r.resolutionState = ResumeForward, Checking
	r.unknownBoundary = last.Outcome == "intent"
	r.execution = x
	r.completed[last.Step] = last.Outcome == "completed"
	return r, nil
}

func (x *execution) proveManagedRoute(ctx context.Context) error {
	e, p := x.executor, x.plan
	routes, ok := e.Routes.(ManagedRecoveryRoutes)
	if !ok {
		return errors.New("managed loaded inspection unavailable")
	}
	facts, err := e.Facts.Read(ctx)
	if err != nil {
		return err
	}
	if facts.Input.Desired.PolicyHash != p.PolicyHash || facts.Input.Desired.PolicyVersion != p.PolicyVersion || !reflect.DeepEqual(facts.Input.State.Target, p.Target) || p.ObservedGeneration.Status != target.KnownStatus || p.ObservedGeneration.Value == nil || *p.ObservedGeneration.Value > ^uint64(0)-2 || facts.Input.State.Generation != *p.ObservedGeneration.Value+2 {
		return errors.New("managed recovery policy or generation changed")
	}
	_, exists, err := e.Releases.CurrentRelease(ctx, p.App)
	if err != nil || exists {
		return errors.New("managed recovery release changed")
	}
	if err := e.Managed.CompletedManaged(ctx, x.managedRouteOwner, p, x.desired); err != nil {
		return err
	}
	if p.Managed.Frontend != nil {
		hash, known := observedUnitHash(facts, p.App, x.unit.Name())
		if !known || hash != x.unit.Hash() || !managedRecoveryFrontendImage(facts, p) {
			return errors.New("managed recovery frontend changed")
		}
		if err := verifyManagedFrontendSecrets(ctx, e, p); err != nil {
			return err
		}
	}
	if err := routes.InspectManagedLoaded(ctx, facts.Routing, p, x.desired); err != nil {
		return err
	}
	if facts.Routing.Files[p.App+".caddy"] == "" {
		return errors.New("managed recovery route absent")
	}
	if x.nextRelease != nil && (x.nextRelease.CaddyGeneration != facts.Routing.Generation || x.nextRelease.CaddyFile.Hash != facts.Routing.Files[p.App+".caddy"]) {
		return errors.New("managed recovery route changed")
	}
	x.facts, x.route = facts, facts.Routing
	x.nextRelease = managedRouteRelease(x.managedRouteOwner, p, x.unit, facts.Routing)
	return ctx.Err()
}

func managedRouteRelease(owner string, p plan.Plan, frontend quadlet.Unit, route caddy.State) *Release {
	m := p.Managed
	engine := target.Unit{Name: "brine-engine-" + string(m.Database.BindingID) + ".service", Hash: "sha256:" + m.EngineUnitHash}
	units := []target.Unit{engine}
	adjunct := &ops.ManagedRelease{BindingID: m.Database.BindingID, EpochID: m.Archive.EpochID, RuntimeSHA256: m.RuntimeSHA256, EngineUnit: engine, EnginePort: m.EnginePort}
	if m.Frontend != nil {
		front := target.Unit{Name: frontend.Name(), Hash: frontend.Hash()}
		units = append(units, front)
		adjunct.Frontend = &ops.ManagedFrontend{Image: m.Frontend.Image, Port: m.Frontend.Port, Unit: front, Secrets: slices.Clone(m.Frontend.Secrets)}
	}
	slices.SortFunc(units, func(a, b target.Unit) int { return strings.Compare(a.Name, b.Name) })
	return &Release{ID: owner, PlanID: p.Hash, Image: p.Image, HostPort: p.HostPort, Secrets: slices.Clone(p.Secrets), Units: units, CaddyFile: target.CaddyFile{Name: p.App + ".caddy", Hash: route.Files[p.App+".caddy"]}, CaddyGeneration: route.Generation, Managed: adjunct}
}

func (x *execution) finishManagedRoute(ctx context.Context, r Recovery) error {
	inspect, cancel := context.WithTimeout(ctx, x.executor.effectTimeout())
	err := x.proveManagedRoute(inspect)
	cancel()
	if err != nil {
		return x.terminal(ctx, RecoveryRequired, &Error{Step: r.Step, Code: "interrupted", Cause: err})
	}
	if x.plan.Managed.Frontend != nil {
		if err := x.check(ctx, x.desired, x.plan.HostPort, false); err != nil {
			return x.terminal(ctx, RecoveryRequired, &Error{Step: r.Step, Code: "interrupted", Cause: err})
		}
		if err := x.check(ctx, x.desired, x.plan.HostPort, true); err != nil {
			return x.terminal(ctx, RecoveryRequired, &Error{Step: r.Step, Code: "interrupted", Cause: err})
		}
	}
	if r.Step == "managed_route_check" && !r.completed[r.Step] {
		if err := x.event(ctx, "managed_route_check", "completed", ""); err != nil {
			return err
		}
	}
	commit := func(ctx context.Context) error {
		if err := x.proveManagedRoute(ctx); err != nil {
			return &Error{Step: "managed_commit", Code: "interrupted", Cause: err}
		}
		var err error
		if x.id == x.managedRouteOwner {
			err = x.executor.Releases.CommitRelease(ctx, x.plan.App, *x.nextRelease)
		} else {
			err = x.executor.Releases.(ManagedResolutionReleases).CommitManagedResolutionRelease(ctx, x.id, x.facts.Input.State.Generation, *x.nextRelease)
		}
		if err == nil {
			return nil
		}
		actual, exists, readErr := x.executor.Releases.CurrentRelease(ctx, x.plan.App)
		if readErr == nil && exists && reflect.DeepEqual(actual, *x.nextRelease) {
			return nil
		}
		return &Error{Step: "managed_commit", Code: "interrupted", Cause: errors.Join(err, readErr)}
	}
	if r.Step != "managed_commit" {
		if err := x.step(ctx, "managed_commit", Committing, "commit_failed", commit); err != nil {
			return x.terminal(ctx, RecoveryRequired, err)
		}
	} else {
		effect, cancel := context.WithTimeout(ctx, x.executor.effectTimeout())
		err := commit(effect)
		cancel()
		if err != nil {
			return x.terminal(ctx, RecoveryRequired, &Error{Step: r.Step, Code: "interrupted", Cause: err})
		}
		if err := x.event(ctx, "managed_commit", "completed", ""); err != nil {
			return err
		}
	}
	return x.terminal(ctx, Succeeded, nil)
}
