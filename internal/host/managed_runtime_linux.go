//go:build linux

package host

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/systemd"
	"slices"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
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
	if err != nil {
		return errEngineSource
	}
	return r.serviceChange(ctx, change, false)
}
func (r managedRuntime) InstallEngine(ctx context.Context, p plan.Plan, previous ops.Release) error {
	permit, err := r.expected(ctx, p)
	if err != nil || r.observer.stopped(ctx, permit) != nil {
		return errEngineSource
	}
	change, err := newManagedServiceChange(permit.Runtime, previous)
	if err != nil {
		return err
	}
	return r.serviceChange(ctx, change, true)
}

func (r managedRuntime) serviceChange(ctx context.Context, change managedServiceChange, install bool) error {
	units := r.units
	if units == nil {
		var err error
		units, err = openManagedUnits(r.home)
		if err != nil {
			return err
		}
		defer func() { _ = units.Close() }()
	}
	var err error
	if install {
		err = units.install(ctx, change)
	} else {
		err = units.stage(ctx, change)
	}
	if errors.Is(err, errManagedServiceUnknown) {
		return &localexec.Error{Kind: localexec.UnknownOutcome}
	}
	return err
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

func (r managedRuntime) CheckManagedIngress(ctx context.Context, claim caddy.ManagedIngressClaim) error {
	projection, err := r.state.ReadManagedApp(ctx, claim.App)
	if err != nil || projection.Permit == nil || projection.Reserved.Database.BindingID != claim.BindingID {
		return errEngineSource
	}
	current := projection.Permit.Runtime
	expected, err := managedRuntimeHash(current)
	if err != nil {
		return errEngineSource
	}
	if claim.Pending && (claim.EpochID != current.EpochID || claim.RuntimeSHA256 != expected) {
		return errEngineSource
	}
	wanted, err := currentManagedDesired(ctx, r.state, current)
	if err != nil || !wanted.EngineRunning || !wanted.IngressServing || claim.Frontend && !wanted.FrontendRunning {
		return errEngineSource
	}
	// Committed routes use current protected native state and their declared
	// compatibility. Historical release runtime/epoch never grant route authority.
	if !slices.Contains(claim.Accepts, current.Module.Schema.Marker) {
		return errEngineSource
	}
	proof, err := r.observer.current(ctx, claim.BindingID, expected)
	if err != nil {
		return errEngineSource
	}
	if claim.Frontend {
		frontend, err := r.state.ReadManagedFrontendPermit(ctx, claim.App)
		if err != nil || frontend.Permit.BindingID != claim.BindingID || frontend.Desired.SpacetimeDB == nil || !slices.Contains(frontend.Desired.SpacetimeDB.Module.Accepts, proof.permit.Runtime.Module.Schema.Marker) {
			return errEngineSource
		}
		if frontend.Pending && (frontend.Permit.RuntimeSHA256 != expected || frontend.Permit.EpochID != current.EpochID) {
			return errEngineSource
		}
		if quadlet.VerifyCurrent(ctx, r.home, claim.App+".container", frontend.Permit.UnitHash) != nil {
			return errEngineSource
		}
		active, err := r.observer.units.IsActive(ctx, mustManagedFrontendUnit(claim.App))
		if err != nil || !active {
			return errEngineSource
		}
	}
	finalDesired, err := currentManagedDesired(ctx, r.state, current)
	if err != nil || finalDesired != wanted {
		return errEngineSource
	}
	return ctx.Err()
}

func mustManagedFrontendUnit(app string) systemd.Unit {
	unit, _ := systemd.ParseUnit(app + ".service")
	return unit
}
