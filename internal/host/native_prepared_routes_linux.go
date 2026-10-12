//go:build linux

package host

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

func nativeCommittedRouteStates(ctx context.Context, service Service, manager *caddy.Manager) (caddy.State, error) {
	if manager == nil {
		return caddy.State{}, errEngineSource
	}
	snapshot, err := service.Inventory.Collect(ctx)
	if err != nil || snapshot.Generation.Status != target.KnownStatus || snapshot.Generation.Value == nil {
		return caddy.State{}, errEngineSource
	}
	state, err := service.Store.LoadBrineState(ctx, snapshot.Identity, *snapshot.Generation.Value)
	if err != nil {
		return caddy.State{}, err
	}
	routes := caddy.State{Generation: *snapshot.Generation.Value, Files: map[string]string{}, Sites: map[string]caddy.Site{}}
	if snapshot.CaddyConfig.Status != target.KnownStatus || snapshot.CaddyConfig.Value == nil {
		return caddy.State{}, errEngineSource
	}
	routes.Generation = snapshot.CaddyConfig.Value.Generation
	for _, release := range state.Releases {
		if release.HostPort > 65535 {
			return caddy.State{}, errEngineSource
		}
		if release.Managed == nil {
			site, err := caddy.CommittedSite(release.Desired, spec.Port(release.HostPort))
			if err != nil {
				return caddy.State{}, err
			}
			routes.Files[release.CaddyFile.Name], routes.Sites[release.CaddyFile.Name] = release.CaddyFile.Hash, site
			continue
		}
		current, err := service.Store.ReadManagedRouteState(ctx, release.App)
		if err != nil {
			return caddy.State{}, err
		}
		if !current.Present {
			continue
		}
		site, err := caddy.CommittedManagedSite(release.Desired, *release.Managed)
		if err != nil {
			return caddy.State{}, err
		}
		routes.Files[release.CaddyFile.Name], routes.Sites[release.CaddyFile.Name] = current.FileHash, site
	}
	if err := manager.CheckDrift(routes); err != nil {
		return caddy.State{}, err
	}
	return routes, nil
}

func (w *nativePrepareWork) withdrawIngress(ctx context.Context, service Service, manager *caddy.Manager) error {
	previous, err := nativeCommittedRouteStates(ctx, service, manager)
	if err != nil {
		return err
	}
	record := store.NativePreparedRoutes{PointID: w.native.PointID(), OperationID: w.operation.ID, App: w.operation.App, PreviousGeneration: previous.Generation, PreviousFiles: previous.Files, CurrentGeneration: previous.Generation, CurrentFiles: previous.Files}
	if err := w.runtime.state.RecordNativePreparedRoutes(ctx, w.native, record, false); err != nil {
		return err
	}
	current := previous
	if _, exists := previous.Files[w.operation.App+".caddy"]; exists {
		routes := apply.GenerationRoutes{Manager: manager, Main: func(ctx context.Context) ([]byte, error) { return trustedRead(ctx, "/etc/caddy/Caddyfile", 1<<20) }}
		outcome, err := routes.Withdraw(ctx, previous, w.operation.App)
		if err != nil || outcome.Outcome != caddy.Applied {
			return errors.Join(errEngineSource, err)
		}
		current = outcome.Next
	}
	if err := manager.InspectLoadedChange(ctx, current, previous); err != nil {
		return err
	}
	record.CurrentGeneration, record.CurrentFiles = current.Generation, current.Files
	if err := w.runtime.state.RecordNativePreparedRoutes(ctx, w.native, record, true); err != nil {
		return err
	}
	w.withdrawn, w.prior = current, previous
	return nil
}

func (w *nativePrepareWork) verifyIngressClosed(ctx context.Context) error {
	if w.runtime.routes == nil {
		return errEngineSource
	}
	if _, exists := w.withdrawn.Files[w.operation.App+".caddy"]; exists {
		return errEngineSource
	}
	return w.runtime.routes.InspectLoadedChange(ctx, w.withdrawn, w.prior)
}

func (r nativeBackupRuntime) preparedRouteStates(ctx context.Context, service Service, pointID, app, targetDeclarationPlanID string) (*caddy.Manager, caddy.State, caddy.State, error) {
	if r.routes == nil {
		return nil, caddy.State{}, caddy.State{}, errEngineSource
	}
	record, err := r.state.ReadNativePreparedRoutes(ctx, pointID)
	planned, desired, planErr := r.state.LoadPlan(ctx, targetDeclarationPlanID)
	if err != nil || planErr != nil || planned.App != app || record.App != app || desired.Name != spec.Name(app) {
		return nil, caddy.State{}, caddy.State{}, errEngineSource
	}
	return r.preparedObservedRouteStates(ctx, service, record)
}
func (r nativeBackupRuntime) preparedObservedRouteStates(ctx context.Context, service Service, record store.NativePreparedRoutes) (*caddy.Manager, caddy.State, caddy.State, error) {
	if r.routes == nil {
		return nil, caddy.State{}, caddy.State{}, errEngineSource
	}
	snapshot, err := service.Inventory.Collect(ctx)
	if err != nil || snapshot.Generation.Status != target.KnownStatus || snapshot.Generation.Value == nil {
		return nil, caddy.State{}, caddy.State{}, errEngineSource
	}
	state, err := r.state.LoadBrineState(ctx, snapshot.Identity, *snapshot.Generation.Value)
	if err != nil {
		return nil, caddy.State{}, caddy.State{}, err
	}
	previous := caddy.State{Generation: record.PreviousGeneration, Files: record.PreviousFiles, Sites: map[string]caddy.Site{}}
	current := caddy.State{Generation: record.CurrentGeneration, Files: record.CurrentFiles, Sites: map[string]caddy.Site{}}
	for _, release := range state.Releases {
		if release.HostPort > 65535 {
			return nil, caddy.State{}, caddy.State{}, errEngineSource
		}
		var site caddy.Site
		if release.Managed != nil {
			site, err = caddy.CommittedManagedSite(release.Desired, *release.Managed)
		} else {
			site, err = caddy.CommittedSite(release.Desired, spec.Port(release.HostPort))
		}
		if err != nil {
			return nil, caddy.State{}, caddy.State{}, err
		}
		if _, exists := previous.Files[release.CaddyFile.Name]; exists {
			previous.Sites[release.CaddyFile.Name] = site
		}
		if _, exists := current.Files[release.CaddyFile.Name]; exists {
			current.Sites[release.CaddyFile.Name] = site
		}
	}
	if err := r.routes.InspectLoadedChange(ctx, current, previous); err != nil {
		return nil, caddy.State{}, caddy.State{}, err
	}
	return r.routes, current, previous, nil
}
