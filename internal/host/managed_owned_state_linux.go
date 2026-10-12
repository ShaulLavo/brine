//go:build linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

// ownedState measures only current process/unit/source ownership for stopping.
// It reads no secret bytes and grants no source verification or live authority.
func (o managedEngineObserver) ownedState(ctx context.Context, permit store.EnginePermit) (string, error) {
	if o.state == nil || o.units == nil || verifyEngineService(ctx, o.home, permit) != nil || !engineAllocatedSource(permit) {
		return "", errEngineSource
	}
	unit, err := systemd.ParseUnit(engineContainerName(permit.Runtime.BindingID) + ".service")
	if err != nil {
		return "", errEngineSource
	}
	host := archiveHost{session: o.session}
	stopped, err := host.serviceStopped(ctx, unit.String(), false)
	if err != nil {
		return "", errEngineSource
	}
	state := "stopped"
	if stopped {
		containers, err := listEngineContainers(ctx, o.session)
		if err != nil || !engineBindingAbsent(containers, permit) {
			return "", errEngineSource
		}
	} else {
		if err := o.ownedRunning(ctx, permit, unit); err != nil {
			return "", err
		}
		state = "running"
	}
	current, err := o.state.ReadEnginePermit(ctx, permit.Runtime.BindingID)
	if err != nil || !sameEnginePermit(current, permit) || verifyEngineService(ctx, o.home, current) != nil {
		return "", errEngineSource
	}
	return state, ctx.Err()
}
func (o managedEngineObserver) ownedRunning(ctx context.Context, permit store.EnginePermit, unit systemd.Unit) error {
	pending, err := o.units.JobPending(ctx, unit)
	if err != nil || pending || verifyEngineLoadedService(ctx, o.session, o.home, unit) != nil {
		return errEngineSource
	}
	properties, err := o.units.Show(ctx, unit)
	if err != nil || properties.ActiveState != "active" || properties.SubState != "running" {
		return errEngineSource
	}
	native, err := o.native(ctx, permit, engineMaterialPaths(o.stateRoot, permit.Runtime))
	if err != nil {
		return errEngineSource
	}
	defer native.close()
	if native.alive() != nil {
		return errEngineSource
	}
	pending, err = o.units.JobPending(ctx, unit)
	if err != nil || pending {
		return errEngineSource
	}
	return ctx.Err()
}
