package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
)

type managedAppFacts struct{ service Service }

func (r managedAppFacts) ManagedFacts(ctx context.Context, input plan.Input) (plan.Input, error) {
	if r.service.Store == nil || r.service.Policy == nil || r.service.Images == nil {
		return plan.Input{}, result.New(result.DependencyMissing, nil)
	}
	if input.Snapshot.Generation.Status != target.KnownStatus || input.Snapshot.Generation.Value == nil || input.State.Target != input.Snapshot.Identity || input.State.Generation != *input.Snapshot.Generation.Value {
		return plan.Input{}, result.New(result.Conflict, nil)
	}
	pol, err := r.service.Policy.Load(ctx)
	if err != nil {
		return plan.Input{}, err
	}
	desired, err := policy.Normalize(App(input.Desired), pol)
	if err != nil {
		return plan.Input{}, err
	}
	facts, err := r.service.managedFacts(ctx, desired, pol, input.Snapshot, input.State)
	return facts.Input, err
}
