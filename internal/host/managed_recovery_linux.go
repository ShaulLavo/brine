//go:build linux

package host

import (
	"context"
	"errors"
	"reflect"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/store"
)

func (r managedRuntime) CompletedManaged(ctx context.Context, operation string, p plan.Plan, d policy.Desired) error {
	permit, err := r.expected(ctx, p)
	if err != nil {
		return errEngineSource
	}
	flags := plan.DesiredComponentsForDeployment(*p.Managed)
	if p.ManagedLifecycle != nil {
		flags = *p.ManagedLifecycle
	}
	wanted, err := currentManagedDesired(ctx, r.state, permit.Runtime)
	if err != nil || wanted.OperationID != operation || wanted.PlanID != p.Hash || wanted.EngineRunning != flags.EngineRunning || wanted.IngressServing != flags.IngressServing || wanted.FrontendRunning != flags.FrontendRunning {
		return errEngineSource
	}
	route, err := r.state.ReadManagedRouteState(ctx, p.App)
	if err != nil || route.OperationID != operation || route.PlanID != p.Hash || route.BindingID != wanted.BindingID || route.EpochID != wanted.EpochID || route.RuntimeSHA256 != wanted.RuntimeSHA256 || route.DesiredRevision != wanted.Revision || route.Present != flags.IngressServing {
		return errEngineSource
	}
	expectedRevision := p.ManagedIntentRevision + 1
	expectedRouteRevision := p.ManagedRouteRevision + 1
	if p.Lifecycle == plan.RestartApp {
		expectedRevision++
		expectedRouteRevision++
	}
	if wanted.Revision != expectedRevision || route.Revision != expectedRouteRevision || r.routes == nil {
		return errEngineSource
	}
	observed, err := r.routes.Observe()
	if err != nil || observed.Generation != route.Generation || observed.Files[p.App+".caddy"] != route.FileHash {
		return errEngineSource
	}
	if err := r.completedComponents(ctx, operation, p, flags); err != nil {
		return err
	}
	final, err := currentManagedDesired(ctx, r.state, permit.Runtime)
	if err != nil || final != wanted || r.completedComponents(ctx, operation, p, flags) != nil {
		return errEngineSource
	}
	return ctx.Err()
}
func (r managedRuntime) completedComponents(ctx context.Context, operation string, p plan.Plan, flags plan.ManagedLifecyclePlan) error {
	stoppingFrontend := p.Lifecycle == plan.StopApp && p.ManagedLifecycle != nil && p.ManagedLifecycle.Frontend
	if stoppingFrontend {
		permit, err := r.expected(ctx, p)
		if err != nil {
			return errEngineSource
		}
		if _, err := r.observer.ownedState(ctx, permit); err != nil {
			return errEngineSource
		}
	}
	if !stoppingFrontend && (flags.EngineRunning && r.EngineReady(ctx, p) != nil || !flags.EngineRunning && r.EngineStopped(ctx, p) != nil) {
		return errEngineSource
	}
	if p.Managed.Frontend == nil {
		return nil
	}
	if !flags.FrontendRunning {
		record, err := r.state.ReadManagedFrontendRecord(ctx, p.App)
		if err != nil || !r.completedFrontendOwner(ctx, record, operation, p) || quadlet.VerifyCurrent(ctx, r.home, p.App+".container", record.Resolution.Permit.UnitHash) != nil {
			return errEngineSource
		}
		host := archiveHost{session: r.observer.session}
		stopped, err := host.serviceStopped(ctx, p.App+".service", false)
		if err != nil || !stopped {
			return errEngineSource
		}
		name, err := podman.ParseName(p.App)
		if err != nil {
			return errEngineSource
		}
		state, err := podman.New(r.observer.session).ContainerState(ctx, name)
		if !managedFrontendStopped(state, err) {
			return errEngineSource
		}
		return ctx.Err()
	}
	return r.completedFrontend(ctx, operation, p)
}
func (r managedRuntime) completedFrontend(ctx context.Context, operation string, p plan.Plan) error {
	record, err := r.state.ReadManagedFrontendRecord(ctx, p.App)
	if err != nil || record.Resolution.Permit.BindingID != p.Managed.Database.BindingID || !r.completedFrontendOwner(ctx, record, operation, p) || quadlet.VerifyCurrent(ctx, r.home, p.App+".container", record.Resolution.Permit.UnitHash) != nil {
		return errEngineSource
	}
	unit := mustManagedFrontendUnit(p.App)
	pending, err := r.observer.units.JobPending(ctx, unit)
	if err != nil || pending {
		return errEngineSource
	}
	props, err := r.observer.units.Show(ctx, unit)
	if err != nil || props.ActiveState != "active" || props.SubState != "running" {
		return errEngineSource
	}
	name, err := podman.ParseName(p.App)
	if err != nil {
		return errEngineSource
	}
	container, err := podman.New(r.observer.session).ContainerState(ctx, name)
	if err != nil || !container.Running {
		return errEngineSource
	}
	final, err := r.state.ReadManagedFrontendRecord(ctx, p.App)
	if err != nil || final.Resolution.Permit != record.Resolution.Permit || quadlet.VerifyCurrent(ctx, r.home, p.App+".container", record.Resolution.Permit.UnitHash) != nil {
		return errEngineSource
	}
	pending, err = r.observer.units.JobPending(ctx, unit)
	if err != nil || pending {
		return errEngineSource
	}
	return ctx.Err()
}

func (r managedRuntime) completedFrontendOwner(ctx context.Context, record store.ManagedFrontendRecord, operation string, p plan.Plan) bool {
	if p.Lifecycle == "" {
		return record.Resolution.Permit.OperationID == operation && record.Resolution.Permit.PlanID == p.Hash && record.Resolution.Permit.RuntimeSHA256 == p.Managed.RuntimeSHA256
	}
	release, err := r.state.CurrentRelease(ctx, p.App)
	if err != nil || release.Managed == nil || release.Managed.Frontend == nil {
		return false
	}
	front := release.Managed.Frontend
	return front.Unit.Hash == record.Resolution.Permit.UnitHash && release.PlanID == record.Resolution.Permit.PlanID && reflect.DeepEqual(front.Image, p.Managed.Frontend.Image) && front.Port == p.Managed.Frontend.Port && reflect.DeepEqual(front.Secrets, p.Managed.Frontend.Secrets)
}

func managedFrontendStopped(state podman.ContainerState, err error) bool {
	if err == nil {
		return !state.Running
	}
	var absent *localexec.Error
	return errors.As(err, &absent) && absent.Kind == localexec.NotFound && absent.ExitCode == 0
}
