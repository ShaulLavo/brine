//go:build linux

package host

import (
	"context"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

// EngineStop is manager-owned cleanup after normal, failed or killed startup.
// Stopping a fenced writer does not require startup or secret-expiry admission.
func (r *EngineStartupRuntime) EngineStop(ctx context.Context, binding string) error {
	if !data.ValidID(binding) {
		return errEngineSource
	}
	permit, err := r.state.ReadEnginePermit(ctx, data.ReplicaBindingID(binding))
	if err != nil {
		return errEngineSource
	}
	cleanup, cancel := context.WithTimeout(ctx, time.Duration(permit.Runtime.StopSeconds+10)*time.Second)
	defer cancel()
	guard, err := acquireEngineStart(cleanup, r.stateRoot, permit.Runtime.BindingID)
	if err != nil {
		return err
	}
	defer func() { _ = guard.close() }()
	current, err := r.state.ReadEnginePermit(cleanup, guard.binding)
	if err != nil || !sameEnginePermit(current, permit) || verifyEngineService(cleanup, r.home, current) != nil || !guard.active() {
		return errEngineSource
	}
	self, err := engineStarterProcess()
	if err != nil || r.stopPostOwned(cleanup, permit, self) != nil {
		return errEngineSource
	}
	return r.stopOwnedNative(cleanup, ctx, permit, self)
}

func (r *EngineStartupRuntime) stopPostOwned(ctx context.Context, permit store.EnginePermit, self systemd.StarterProcess) error {
	current, err := engineStarterProcess()
	if err != nil || current != self {
		return errEngineSource
	}
	unit, err := systemd.ParseUnit(engineContainerName(permit.Runtime.BindingID) + ".service")
	if err != nil || systemd.New(r.session).StopPostInvocation(ctx, unit, self) != nil {
		return errEngineSource
	}
	return verifyEngineLoadedService(ctx, r.session, r.home, unit)
}

func (r *EngineStartupRuntime) stopOwnedNative(ctx, lifetime context.Context, permit store.EnginePermit, self systemd.StarterProcess) error {
	containers, err := listEngineContainers(ctx, r.session)
	if err != nil {
		return err
	}
	if engineBindingAbsent(containers, permit) {
		return nil
	}
	for _, container := range containers {
		if container.Name != engineContainerName(permit.Runtime.BindingID) {
			continue
		}
		native, err := bindEngineNative(container, permit, engineMaterialPaths(r.stateRoot, permit.Runtime))
		if err != nil {
			r.retainEngineLifetime(lifetime, permit, nil)
			return errEngineSource
		}
		defer native.close()
		if native.alive() != nil || r.stopPostOwned(ctx, permit, self) != nil {
			r.retainEngineLifetime(lifetime, permit, native)
			return errEngineSource
		}
		if err = r.stopLiveEngine(ctx, permit); err != nil || !native.exited() {
			r.retainEngineLifetime(lifetime, permit, native)
			return errEngineSource
		}
		return nil
	}
	r.retainEngineLifetime(lifetime, permit, nil)
	return errEngineSource
}
