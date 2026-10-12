package apps

import (
	"context"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
)

type ManagedFactsReader interface {
	ManagedFacts(context.Context, policy.Desired) (plan.Input, error)
}

func (s Service) collectManaged(ctx context.Context, in *plan.Input) error {
	if in.Desired.SpacetimeDB == nil {
		return nil
	}
	if s.ManagedFacts == nil {
		return result.New(result.DependencyMissing, nil)
	}
	fresh, err := s.ManagedFacts.ManagedFacts(ctx, in.Desired)
	if err != nil {
		return err
	}
	*in = fresh
	return nil
}
