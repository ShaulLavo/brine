package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

type managedAppFacts struct{ service Service }

func (r managedAppFacts) ManagedFacts(ctx context.Context, desired policy.Desired) (plan.Input, error) {
	facts, err := r.service.facts(ctx, App(desired))
	return facts.Input, err
}
