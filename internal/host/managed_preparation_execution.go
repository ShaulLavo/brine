package host

import (
	"bytes"
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
)

func (e Executor) runManagedPreparation(ctx context.Context, operation string, p plan.Plan, d policy.Desired) error {
	if e.Service.Store == nil || p.Managed == nil {
		return result.New(result.Conflict, nil)
	}
	facts, err := e.Service.facts(ctx, App(d))
	if err != nil {
		return err
	}
	inspector, ok := e.Service.Data.(preparationInspector)
	if !ok {
		return result.New(result.DependencyMissing, nil)
	}
	roots, err := inspector.InspectPreparationRoots(ctx, facts.Input.Desired)
	if err != nil || len(roots) != 1 {
		return errors.Join(result.New(result.Conflict, nil), err)
	}
	fresh, err := plan.BuildManagedPreparation(facts.Input, *p.Managed, roots[0])
	if err != nil {
		return err
	}
	approved, err := p.CanonicalBytes()
	if err != nil {
		return err
	}
	rebuilt, err := fresh.CanonicalBytes()
	if err != nil || !bytes.Equal(approved, rebuilt) {
		return result.New(result.Conflict, errors.Join(err, ops.RecordPlanDrift(ctx, e.Service.Store, operation, p, fresh)))
	}
	engine := e.Engine
	engine.Facts = fixedFacts{facts}
	return engine.Run(ctx, operation, p, d)
}
