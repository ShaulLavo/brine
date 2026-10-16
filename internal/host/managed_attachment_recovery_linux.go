//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
)

func (r managedRuntime) InterruptedFrontend(ctx context.Context, owner string, p plan.Plan, d policy.Desired) error {
	if p.Managed == nil || p.Managed.Frontend == nil || p.Image.ManifestDigest.Value == nil || r.EngineReady(ctx, p) != nil {
		return errEngineSource
	}
	wanted, err := r.state.ReadManagedDesired(ctx, p.Managed.Database.BindingID)
	if err != nil || wanted != managedDesiredRecord(owner, p, p.ManagedIntentRevision+1, plan.DesiredComponentsForDeployment(*p.Managed)) {
		return errEngineSource
	}
	record, err := r.state.ReadManagedFrontendRecord(ctx, p.App)
	if err != nil || !r.completedFrontendOwner(ctx, record, owner, p) {
		return errEngineSource
	}
	unit, err := quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
	if err != nil || record.Resolution.Permit.UnitHash != unit.Hash() || quadlet.VerifyCurrent(ctx, r.home, unit.Name(), unit.Hash()) != nil || verifyManagedFrontendLoaded(ctx, r.observer.session, r.home, p.App) != nil {
		return errEngineSource
	}
	stopped, err := (archiveHost{session: r.observer.session}).serviceStopped(ctx, p.App+".service", false)
	if err != nil || !stopped {
		return errEngineSource
	}
	name, err := managedFrontendContainerName(p.App)
	if err != nil {
		return errEngineSource
	}
	container, err := podman.New(r.observer.session).ContainerState(ctx, name)
	if !managedFrontendStopped(container, err) {
		return errEngineSource
	}
	if r.routes == nil {
		return errEngineSource
	}
	route, err := r.state.ReadManagedRouteState(ctx, p.App)
	if err != nil || route.Revision != p.ManagedRouteRevision {
		return errEngineSource
	}
	priorPlan, _, err := r.state.LoadPlan(ctx, route.PlanID)
	if err != nil || priorPlan.Managed == nil || priorPlan.Managed.Frontend != nil {
		return errEngineSource
	}
	observed, err := r.routes.Observe()
	if err != nil || observed.Generation != route.Generation || observed.Files[p.App+".caddy"] != route.FileHash || r.routes.VerifyLoaded(ctx, observed) != nil {
		return errEngineSource
	}
	current, err := r.state.ReadManagedFrontendRecord(ctx, p.App)
	if err != nil || current.Resolution.Permit != record.Resolution.Permit {
		return errEngineSource
	}
	final, err := r.state.ReadManagedDesired(ctx, p.Managed.Database.BindingID)
	if err != nil || final != wanted {
		return errEngineSource
	}
	return ctx.Err()
}
