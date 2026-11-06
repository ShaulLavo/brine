package apps

import (
	"context"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
)

func (s Service) managedRollback(ctx context.Context, snapshot target.Snapshot, state plan.BrineState, current plan.CurrentRelease, release ops.Release, desired policy.Desired) (RollbackPlan, error) {
	if s.ManagedFacts == nil || s.LoadPolicy == nil || current.Managed == nil || release.Managed == nil || release.Managed.Frontend == nil || current.Managed.Frontend == nil || current.Managed.Database.BindingID != release.Managed.BindingID {
		return RollbackPlan{}, result.New(result.Conflict, nil)
	}
	facts, err := s.ManagedFacts.ManagedFacts(ctx, plan.Input{Desired: current.Desired, Snapshot: snapshot, State: state, Image: current.Image})
	if err != nil {
		return RollbackPlan{}, err
	}
	if facts.Managed == nil || facts.Managed.NativeState.Status != target.KnownStatus || facts.Managed.NativeState.Value == nil || *facts.Managed.NativeState.Value != "running" {
		return RollbackPlan{}, result.New(result.Conflict, nil)
	}
	client, err := plan.ManagedClientDesired(facts, desired)
	if err != nil {
		return RollbackPlan{}, result.New(result.Conflict, err)
	}
	pol, err := s.LoadPolicy(ctx)
	if err != nil {
		return RollbackPlan{}, err
	}
	client, err = policy.Normalize(client.App(), pol)
	if err != nil {
		return RollbackPlan{}, err
	}
	facts.Desired = client
	facts, err = s.ManagedFacts.ManagedFacts(ctx, facts)
	if err != nil {
		return RollbackPlan{}, err
	}
	if facts.Snapshot.Identity != snapshot.Identity || facts.State.Target != state.Target {
		return RollbackPlan{}, result.New(result.Conflict, nil)
	}
	facts.SecretExact = append([]plan.SecretBinding{}, release.Secrets...)
	p, err := plan.Build(facts)
	if err != nil {
		return RollbackPlan{}, err
	}
	if p.Managed == nil || facts.Managed == nil || facts.Managed.Runtime == nil || p.Managed.Database.BindingID != current.Managed.Database.BindingID {
		return RollbackPlan{}, result.New(result.Conflict, nil)
	}
	if p.Kind == plan.NoOp {
		return RollbackPlan{}, result.New(result.RollbackNoOp, nil)
	}
	id, err := s.Store.SavePlan(ctx, p, facts.Desired)
	if err != nil {
		return RollbackPlan{}, err
	}
	return RollbackPlan{PlanID: id, ReleaseID: release.ID, Compatibility: "managed_current_compatible", Kind: p.Kind, Diff: p.Diff, Conflicts: p.Conflicts}, nil
}
