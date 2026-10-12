package apps

import (
	"context"
	"errors"

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
	facts, err := s.ManagedFacts.ManagedFacts(ctx, current.Desired)
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
	facts, err = s.ManagedFacts.ManagedFacts(ctx, client)
	if err != nil {
		return RollbackPlan{}, err
	}
	if facts.Snapshot.Identity != snapshot.Identity || facts.State.Target != state.Target {
		return RollbackPlan{}, result.New(result.Conflict, nil)
	}
	// Preserve the target frontend's retained immutable secret versions. Versions
	// absent from fresh owned inventory refuse; rollback never substitutes latest.
	if err := selectManagedRollbackSecrets(&facts, release); err != nil {
		return RollbackPlan{}, err
	}
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

func selectManagedRollbackSecrets(in *plan.Input, release ops.Release) error {
	if in.Snapshot.Apps.Status != target.KnownStatus || in.Snapshot.Apps.Value == nil {
		return result.New(result.Conflict, nil)
	}
	for i := range *in.Snapshot.Apps.Value {
		app := &(*in.Snapshot.Apps.Value)[i]
		if app.Name != string(in.Desired.Name) {
			continue
		}
		if app.Secrets.Status != target.KnownStatus || app.Secrets.Value == nil {
			return result.New(result.Conflict, nil)
		}
		selected := []target.Secret{}
		for _, bound := range release.Secrets {
			found := false
			for _, secret := range *app.Secrets.Value {
				if secret.Name == bound.VersionName && secret.ID == bound.ID {
					selected = append(selected, secret)
					found = true
				}
			}
			if !found {
				return result.New(result.Conflict, nil)
			}
		}
		app.Secrets = target.Known(selected)
		return nil
	}
	return errors.New("managed rollback frontend inventory unavailable")
}
