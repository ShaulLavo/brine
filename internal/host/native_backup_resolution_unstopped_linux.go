//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

func (o nativeBackupResolutionOperations) inspectUnstoppedPreparation(ctx context.Context, i *store.NativeBackupInspection, v *nativeBackupObserved, expected store.NativeBackupPreparedExpectation) error {
	backup, err := o.service.Store.ReadNativeBackupPolicy(ctx, i.BindingID())
	pol, policyErr := o.service.Policy.Load(ctx)
	if err != nil || policyErr != nil || pol.Hash() != "sha256:"+backup.PolicySHA256 {
		return errEngineSource
	}
	manager, current, previous, err := o.runtime.preparedObservedRouteStates(ctx, o.service, expected.Routes)
	if err != nil {
		return err
	}
	if _, present := current.Files[expected.Routes.App+".caddy"]; present {
		return errEngineSource
	}
	front := expected.Release.Managed.Frontend
	if front != nil {
		if quadlet.VerifyCurrent(ctx, o.runtime.home, front.Unit.Name, front.Unit.Hash) != nil || verifyManagedFrontendLoaded(ctx, o.runtime.session, o.runtime.home, expected.Routes.App) != nil {
			return errEngineSource
		}
	}
	v.frontendRunning, err = o.inspectUnstoppedFrontend(ctx, expected)
	if err != nil {
		return err
	}
	observer := managedEngineObserver{state: o.runtime.state, stateRoot: o.runtime.stateRoot, home: o.runtime.home, session: o.runtime.session, units: systemd.New(o.runtime.session)}
	engineState, err := observer.ownedState(ctx, expected.Permit)
	if err != nil {
		return err
	}
	if engineState == "stopped" {
		v.lifetime, err = data.AcquireEngineLifetimeLock(ctx, o.runtime.stateRoot, i.BindingID())
		if err != nil {
			return err
		}
	}
	if o.service.managedLoggingFacts == nil {
		return errEngineSource
	}
	_, root, loaded, err := o.service.managedLoggingFacts(ctx)
	if err != nil || !data.ValidCatalogHash(root) || !data.ValidCatalogHash(loaded) {
		return errEngineSource
	}
	if err = manager.InspectLoadedChange(ctx, current, previous); err != nil {
		return err
	}
	final, err := i.PreparedExpectation(ctx)
	firstRaw, _ := json.Marshal(expected)
	finalRaw, _ := json.Marshal(final)
	if err != nil || string(firstRaw) != string(finalRaw) {
		return errors.Join(errEngineSource, err)
	}
	v.stopNotIssued = true
	v.unitsState = engineState
	v.unstopped = expected
	v.rootSHA256 = root
	v.loadedSHA256 = loaded
	return nil
}

func (o nativeBackupResolutionOperations) inspectUnstoppedFrontend(ctx context.Context, expected store.NativeBackupPreparedExpectation) (bool, error) {
	app := expected.Routes.App
	if expected.Release.Managed.Frontend == nil {
		stopped, err := (archiveHost{session: o.runtime.session}).stoppedOrAbsentService(ctx, app+".service")
		if err != nil || !stopped {
			return false, errEngineSource
		}
		name, err := managedFrontendContainerName(app)
		if err != nil {
			return false, err
		}
		state, err := podman.New(o.runtime.session).ContainerState(ctx, name)
		if !managedFrontendStopped(state, err) {
			return false, errEngineSource
		}
		return false, nil
	}
	unit, err := systemd.ParseUnit(app + ".service")
	if err != nil {
		return false, err
	}
	manager := systemd.New(o.runtime.session)
	pending, err := manager.JobPending(ctx, unit)
	if err != nil || pending {
		return false, errEngineSource
	}
	props, err := manager.Show(ctx, unit)
	if err != nil {
		return false, errEngineSource
	}
	name, err := managedFrontendContainerName(app)
	if err != nil {
		return false, err
	}
	state, stateErr := podman.New(o.runtime.session).ContainerState(ctx, name)
	if props.ActiveState == "active" && props.SubState == "running" && stateErr == nil && state.Running && !expected.FrontendStopped {
		return true, nil
	}
	if inspectPreparedUnitStopped(ctx, archiveHost{session: o.runtime.session}, unit.String()) != nil || !managedFrontendStopped(state, stateErr) {
		return false, errEngineSource
	}
	return false, nil
}
