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

// ManagedRecoveryFrontend proves current owned components and cached pinned frontend artifacts with the frontend stopped.
type ManagedRecoveryFrontend interface {
	StoppedManagedFrontend(context.Context, string, plan.Plan, policy.Desired) error
}

// ManagedResolutionReleases commits the original owner's proven artifacts without rebinding live permits.
type ManagedResolutionReleases interface {
	CommitManagedResolutionRelease(context.Context, string, uint64, Release) error
}

type ManagedRecoveryRouteReceipt interface {
	UnrecordedManagedRoute(context.Context, string, plan.Plan, policy.Desired) error
	RecordRecoveredManagedRoute(context.Context, string, string, plan.Plan, policy.Desired, caddy.State) error
}

func (e *Executor) inspectManagedRoute(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, cursor ops.ManagedCreateCursor, r Recovery) (Recovery, error) {
	last := cursor.Boundary
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
	x := &execution{executor: e, id: op.ID, plan: p, desired: d, state: op.State, managedRouteOwner: owner, managedFrontendStart: cursor.FrontendStart, managedFrontendWake: cursor.FrontendStart.Step == "" && last.Step == "managed_route_check" && (last.Outcome == "intent" || last.Outcome == "unknown")}
	if last.Step == "managed_route_receipt" {
		x.managedRouteReceipt = last
	}
	if x.managedRouteReceipt.Step != "" && owner == op.ID {
		return r, nil
	}
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
	if err := x.proveManagedRoute(inspect, true); err != nil {
		return r, nil
	}
	if x.managedFrontendStopped && !x.managedFrontendWake {
		return r, nil
	}
	r.Action, r.resolutionState = ResumeForward, managedRecoveryState(last.Step)
	if x.managedFrontendStopped {
		r.resolutionState = Starting
	}
	r.unknownBoundary = last.Outcome == "intent"
	r.execution = x
	r.completed[last.Step] = last.Outcome == "completed"
	return r, nil
}

func (x *execution) proveManagedRoute(ctx context.Context, allowStopped bool) error {
	e, p := x.executor, x.plan
	routes, ok := e.Routes.(ManagedRecoveryRoutes)
	if !ok {
		return errors.New("managed loaded inspection unavailable")
	}
	facts, err := e.Facts.Read(ctx)
	if err != nil {
		return err
	}
	if facts.Input.Desired.PolicyHash != p.PolicyHash || facts.Input.Desired.PolicyVersion != p.PolicyVersion || !reflect.DeepEqual(facts.Input.State.Target, p.Target) || p.ObservedGeneration.Status != target.KnownStatus || p.ObservedGeneration.Value == nil || *p.ObservedGeneration.Value > ^uint64(0)-2 {
		return errors.New("managed recovery policy or generation changed")
	}
	unrecorded := x.managedRouteReceipt.Step != "" && x.managedRouteReceipt.Outcome != "completed" && facts.Input.State.Generation == *p.ObservedGeneration.Value+1
	if !unrecorded && facts.Input.State.Generation != *p.ObservedGeneration.Value+2 {
		return errors.New("managed recovery generation changed")
	}
	_, exists, err := e.Releases.CurrentRelease(ctx, p.App)
	if err != nil || exists {
		return errors.New("managed recovery release changed")
	}
	x.managedFrontendStopped = false
	if unrecorded {
		receipt, ok := e.Managed.(ManagedRecoveryRouteReceipt)
		if !ok || x.id == x.managedRouteOwner {
			return errors.New("managed route receipt recovery unavailable")
		}
		if err := receipt.UnrecordedManagedRoute(ctx, x.managedRouteOwner, p, x.desired); err != nil {
			return err
		}
	} else if err := e.Managed.CompletedManaged(ctx, x.managedRouteOwner, p, x.desired); err != nil {
		stopped, ok := e.Managed.(ManagedRecoveryFrontend)
		if !allowStopped || !ok || x.id == x.managedRouteOwner || p.Managed.Frontend == nil || stopped.StoppedManagedFrontend(ctx, x.managedRouteOwner, p, x.desired) != nil {
			return err
		}
		x.managedFrontendStopped = true
	}
	if p.Managed.Frontend != nil {
		hash, known := observedUnitHash(facts, p.App, x.unit.Name())
		if !known || hash != x.unit.Hash() || !x.managedFrontendStopped && !managedRecoveryFrontendImage(facts, p) {
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
	err := x.proveManagedRoute(inspect, true)
	cancel()
	if err != nil {
		return x.terminal(ctx, RecoveryRequired, &Error{Step: r.Step, Code: "interrupted", Cause: err})
	}
	if r.Step == "managed_route_receipt" {
		if err := x.recoverManagedRouteReceipt(ctx); err != nil {
			return x.terminal(ctx, RecoveryRequired, &Error{Step: r.Step, Code: "interrupted", Cause: err})
		}
		r.Step = "managed_route_check"
	}
	if err := x.wakeManagedFrontend(ctx); err != nil {
		return x.terminal(ctx, RecoveryRequired, err)
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
		if err := x.proveManagedRoute(ctx, false); err != nil {
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

func (x *execution) recoverManagedRouteReceipt(ctx context.Context) error {
	if x.facts.Input.State.Generation == *x.plan.ObservedGeneration.Value+1 {
		receipt, ok := x.executor.Managed.(ManagedRecoveryRouteReceipt)
		if !ok {
			return errors.New("managed route receipt recovery unavailable")
		}
		effect, cancel := context.WithTimeout(ctx, x.executor.effectTimeout())
		err := receipt.RecordRecoveredManagedRoute(effect, x.id, x.managedRouteOwner, x.plan, x.desired, x.route)
		cancel()
		if err != nil {
			return err
		}
	}
	if x.managedRouteReceipt.Outcome != "completed" {
		if err := x.event(ctx, "managed_route_receipt", "completed", ""); err != nil {
			return err
		}
	}
	return x.event(ctx, "managed_route_check", "intent", "")
}

func (x *execution) wakeManagedFrontend(ctx context.Context) error {
	prior := x.managedFrontendStart
	if prior.Step != "" && x.managedFrontendStopped {
		return &Error{Step: prior.Step, Code: "interrupted", Cause: errors.New("unknown frontend start cannot be repeated")}
	}
	if x.managedFrontendStopped && !x.managedFrontendWake {
		return &Error{Step: "managed_route_check", Code: "interrupted", Cause: errors.New("frontend wake boundary was not admitted")}
	}
	if x.managedFrontendStopped {
		if err := x.step(ctx, "managed_recovery_frontend_start", Starting, "start_failed", func(ctx context.Context) error {
			if err := x.proveManagedRoute(ctx, true); err != nil || !x.managedFrontendStopped {
				return &Error{Step: "managed_recovery_frontend_start", Code: "interrupted", Cause: err}
			}
			return x.executor.Systemd.Start(ctx, x.service)
		}); err != nil {
			return err
		}
		x.managedFrontendStopped = false
	}
	if prior.Step != "" && prior.Outcome != "completed" {
		if prior.Outcome == "intent" {
			if err := x.event(ctx, prior.Step, "unknown", "interrupted"); err != nil {
				return err
			}
		}
		if err := x.event(ctx, prior.Step, "completed", ""); err != nil {
			return err
		}
	}
	if err := x.prepareResolution(ctx, Checking); err != nil {
		return err
	}
	inspect, cancel := context.WithTimeout(ctx, x.executor.effectTimeout())
	defer cancel()
	return x.proveManagedRoute(inspect, false)
}
