//go:build linux

package host

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
)

func (r managedRuntime) UnrecordedManagedRoute(ctx context.Context, operation string, p plan.Plan, d policy.Desired) error {
	if p.Kind != plan.Create || p.Lifecycle != "" || p.Managed == nil || r.routes == nil {
		return errEngineSource
	}
	permit, err := r.expected(ctx, p)
	if err != nil {
		return err
	}
	flags := plan.DesiredComponentsForDeployment(*p.Managed)
	wanted, err := currentManagedDesired(ctx, r.state, permit.Runtime)
	if err != nil || wanted.OperationID != operation || wanted.PlanID != p.Hash || wanted.Revision != p.ManagedIntentRevision+1 || wanted.EngineRunning != flags.EngineRunning || wanted.IngressServing != flags.IngressServing || wanted.FrontendRunning != flags.FrontendRunning {
		return errEngineSource
	}
	previous, err := r.state.ReadManagedRouteState(ctx, p.App)
	if err != nil && !errors.Is(err, store.ErrNotFound) || err == nil && previous.Revision != p.ManagedRouteRevision || errors.Is(err, store.ErrNotFound) && p.ManagedRouteRevision != 0 {
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

func (r managedRuntime) RecordRecoveredManagedRoute(ctx context.Context, resolver, owner string, p plan.Plan, d policy.Desired, observed caddy.State) error {
	if err := r.UnrecordedManagedRoute(ctx, owner, p, d); err != nil {
		return err
	}
	if r.loggingFacts == nil || r.routes.InspectLoadedChange(ctx, observed, observed) != nil {
		return errEngineSource
	}
	_, root, loaded, err := r.loggingFacts(ctx)
	if err != nil {
		return errEngineSource
	}
	hash, present := observed.Files[p.App+".caddy"]
	if !present {
		return errEngineSource
	}
	wanted := store.ManagedRouteState{App: p.App, BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, DesiredRevision: p.ManagedIntentRevision + 1, PlanID: p.Hash, OperationID: owner, Revision: p.ManagedRouteRevision + 1, Generation: observed.Generation, Present: true, FileHash: hash, RootSHA256: root, LoadedSHA256: loaded}
	_, err = r.state.CommitManagedResolutionRouteState(ctx, resolver, p.ManagedRouteRevision, wanted)
	return err
}
