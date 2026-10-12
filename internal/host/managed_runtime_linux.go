//go:build linux

package host

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/store"
)

type managedRuntime struct {
	state    *store.Store
	units    *managedUnits
	observer managedEngineObserver
	home     string
}

func (r managedRuntime) expected(ctx context.Context, p plan.Plan) (store.EnginePermit, error) {
	if r.state == nil || p.Managed == nil || !p.Managed.Valid() || p.Managed.State != data.EngineInitialized {
		return store.EnginePermit{}, errEngineSource
	}
	permit, err := r.state.ReadEnginePermit(ctx, p.Managed.Database.BindingID)
	if err != nil {
		return permit, errEngineSource
	}
	fingerprint, err := managedRuntimeHash(permit.Runtime)
	if err != nil || fingerprint != p.Managed.RuntimeSHA256 || permit.Reserved.Database != p.Managed.Database || permit.Reserved.Archive != p.Managed.Archive || permit.Runtime.UnitSHA256 != p.Managed.EngineUnitHash {
		return permit, errEngineSource
	}
	return permit, nil
}
func (r managedRuntime) StageEngine(ctx context.Context, p plan.Plan, previous ops.Release) error {
	permit, err := r.expected(ctx, p)
	if err != nil {
		return err
	}
	change, err := newManagedServiceChange(permit.Runtime, previous)
	if err != nil || r.units == nil {
		return errEngineSource
	}
	return r.units.stage(ctx, change)
}
func (r managedRuntime) InstallEngine(ctx context.Context, p plan.Plan, previous ops.Release) error {
	permit, err := r.expected(ctx, p)
	if err != nil || r.units == nil || r.observer.stopped(ctx, permit) != nil {
		return errEngineSource
	}
	change, err := newManagedServiceChange(permit.Runtime, previous)
	if err != nil {
		return err
	}
	return r.units.install(ctx, change)
}
func (r managedRuntime) VerifyEngineUnit(ctx context.Context, p plan.Plan) error {
	permit, err := r.expected(ctx, p)
	if err != nil {
		return err
	}
	return verifyEngineService(ctx, r.home, permit)
}
func (r managedRuntime) BeforeEngineStart(ctx context.Context, p plan.Plan) error {
	permit, err := r.expected(ctx, p)
	if err != nil {
		return err
	}
	if verifyEngineService(ctx, r.home, permit) != nil {
		return errEngineSource
	}
	return r.observer.stopped(ctx, permit)
}
func (r managedRuntime) EngineReady(ctx context.Context, p plan.Plan) error {
	if _, err := r.expected(ctx, p); err != nil {
		return err
	}
	_, err := r.observer.current(ctx, p.Managed.Database.BindingID, p.Managed.RuntimeSHA256)
	return err
}
func (r managedRuntime) BindFrontend(ctx context.Context, operation string, p plan.Plan, unitHash string) error {
	if r.EngineReady(ctx, p) != nil {
		return errEngineSource
	}
	_, err := r.state.BindManagedFrontendPermit(ctx, operation, p.Hash, unitHash)
	return err
}
func (r managedRuntime) RestoreFrontendPermit(ctx context.Context, operation, previous string) error {
	return r.state.RestoreManagedFrontendPermit(ctx, operation, previous)
}
func (r managedRuntime) BeforeFrontendStart(ctx context.Context, operation string, p plan.Plan, d policy.Desired, unitHash string) error {
	if p.Managed == nil || p.Managed.Frontend == nil || d.SpacetimeDB == nil || r.EngineReady(ctx, p) != nil {
		return errEngineSource
	}
	permit, err := r.state.ReadManagedFrontendPermit(ctx, p.App)
	if err != nil || permit.Permit.OperationID != operation || permit.Permit.PlanID != p.Hash || permit.Permit.UnitHash != unitHash || quadlet.VerifyCurrent(ctx, r.home, p.App+".container", unitHash) != nil {
		return errEngineSource
	}
	return ctx.Err()
}
func (r managedRuntime) BeforeIngress(ctx context.Context, operation string, p plan.Plan, d policy.Desired) error {
	if r.EngineReady(ctx, p) != nil {
		return errEngineSource
	}
	if p.Managed.Frontend == nil {
		return nil
	}
	permit, err := r.state.ReadManagedFrontendPermit(ctx, p.App)
	if err != nil || permit.Permit.OperationID != operation || permit.Permit.PlanID != p.Hash {
		return errors.Join(errEngineSource, err)
	}
	return ctx.Err()
}

func (r managedRuntime) FrontendPermitOwner(ctx context.Context, app string) (string, error) {
	permit, err := r.state.ReadManagedFrontendPermit(ctx, app)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return permit.Permit.OperationID, nil
}
