//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

// ManagedFrontendRuntime reuses the narrowly read-only engine startup bundle.
// It opens no deployment, secret-write, route-write, or schema migration service.
type ManagedFrontendRuntime struct {
	startup *EngineStartupRuntime
	uid     uint32
}

func OpenManagedFrontendPermit(ctx context.Context) (*ManagedFrontendRuntime, error) {
	startup, err := OpenEngineStartup(ctx)
	if err != nil {
		return nil, err
	}
	return &ManagedFrontendRuntime{startup: startup, uid: uint32(os.Geteuid())}, nil //nolint:gosec // OpenEngineStartup has validated the non-root UID fits uint32.
}
func (r *ManagedFrontendRuntime) Close() error { return r.startup.Close() }

func (r *ManagedFrontendRuntime) Permit(ctx context.Context, binding, app string) (resultErr error) {
	if r == nil || r.startup == nil || !data.ValidID(binding) {
		return errEngineSource
	}
	runtime := r.startup
	resolution, err := runtime.state.ReadManagedFrontendPermit(ctx, app)
	if err != nil || resolution.Pending && resolution.PendingOperationID == "" || resolution.Permit.BindingID != data.ReplicaBindingID(binding) {
		return errEngineSource
	}
	operation := attemptOperation{uid: r.uid, home: runtime.home}
	if err := verifyManagedFrontendPending(ctx, resolution, DiskPolicy{}, operation.OperationActive); err != nil {
		return err
	}
	guard, err := data.AcquireEngineLaunchGuard(ctx, runtime.stateRoot, resolution.Permit.BindingID)
	if err != nil {
		return errEngineSource
	}
	defer func() { resultErr = errors.Join(resultErr, guard.Release()) }()
	if quadlet.VerifyCurrent(ctx, runtime.home, app+".container", resolution.Permit.UnitHash) != nil || verifyManagedFrontendLoaded(ctx, runtime.session, runtime.home, app) != nil {
		return errEngineSource
	}
	observer := managedEngineObserver{state: runtime.state, stateRoot: runtime.stateRoot, home: runtime.home, session: runtime.session, units: systemd.New(runtime.session)}
	expected := resolution.Permit.RuntimeSHA256
	if !resolution.Pending {
		current, readErr := runtime.state.ReadEnginePermit(ctx, resolution.Permit.BindingID)
		if readErr != nil {
			return errEngineSource
		}
		expected, readErr = managedRuntimeHash(current.Runtime)
		if readErr != nil {
			return errEngineSource
		}
	}
	proof, err := observer.current(ctx, resolution.Permit.BindingID, expected)
	if err != nil || resolution.Pending && proof.permit.Runtime.EpochID != resolution.Permit.EpochID || resolution.Desired.SpacetimeDB == nil || !slices.Contains(resolution.Desired.SpacetimeDB.Module.Accepts, proof.permit.Runtime.Module.Schema.Marker) {
		return errEngineSource
	}
	wanted, err := currentManagedDesired(ctx, runtime.state, proof.permit.Runtime)
	if err != nil || !wanted.EngineRunning || !wanted.FrontendRunning {
		return errEngineSource
	}
	final, err := runtime.state.ReadManagedFrontendPermit(ctx, app)
	if err != nil || final.Permit != resolution.Permit || final.Pending != resolution.Pending || final.PendingOperationID != resolution.PendingOperationID || quadlet.VerifyCurrent(ctx, runtime.home, app+".container", resolution.Permit.UnitHash) != nil || verifyManagedFrontendLoaded(ctx, runtime.session, runtime.home, app) != nil {
		return errEngineSource
	}
	finalDesired, err := currentManagedDesired(ctx, runtime.state, proof.permit.Runtime)
	if err != nil || finalDesired != wanted {
		return errEngineSource
	}
	if err := verifyManagedFrontendPending(ctx, final, DiskPolicy{}, operation.OperationActive); err != nil {
		return err
	}
	return ctx.Err()
}

func verifyManagedFrontendPending(ctx context.Context, resolution store.ManagedFrontendResolution, loader PolicyLoader, active func(context.Context, string) (bool, error)) error {
	if !resolution.Pending {
		return nil
	}
	if resolution.PendingOperationID == "" {
		return errEngineSource
	}
	pol, err := loader.Load(ctx)
	if err != nil || pol.Hash() != resolution.Plan.PolicyHash {
		return errEngineSource
	}
	running, err := active(ctx, resolution.PendingOperationID)
	if err != nil || !running {
		return errEngineSource
	}
	return ctx.Err()
}
