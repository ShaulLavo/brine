package apply

import (
	"bytes"
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/systemd"
)

func (r *managedExecution) preflight(ctx context.Context) error {
	x := r.x
	e, p, d := x.executor, x.plan, x.desired

	if e.Managed == nil || e.Facts == nil || e.Releases == nil || e.Systemd == nil || e.Routes == nil || p.Managed == nil || !p.Managed.Valid() || p.Managed.State != data.EngineInitialized || !desiredMatches(p, d) || p.Kind != plan.Create && p.Kind != plan.Update && p.Kind != plan.NoOp || len(p.Conflicts) != 0 {
		return errors.New("managed deployment input required")
	}
	facts, err := e.Facts.Read(ctx)
	if err != nil {
		return err
	}
	fresh, err := plan.Build(facts.Input)
	if p.Lifecycle == plan.StopApp && p.ManagedLifecycle != nil && p.ManagedLifecycle.Engine {
		fresh, err = plan.BuildEngineStop(facts.Input)
	} else if p.Lifecycle != "" {
		fresh, err = plan.BuildLifecycle(facts.Input, p.Lifecycle)
	}
	if err != nil {
		return err
	}
	approved, err := p.CanonicalBytes()
	if err != nil {
		return err
	}
	rebuilt, err := fresh.CanonicalBytes()
	if err != nil || !bytes.Equal(approved, rebuilt) {
		return errors.Join(err, errors.New("managed plan drift"), ops.RecordPlanDrift(ctx, e.Journal, x.id, p, fresh))
	}
	x.facts = facts
	x.previous, r.hadRelease, err = e.Releases.CurrentRelease(ctx, p.App)
	if err != nil {
		return err
	}
	if p.Kind == plan.Create && r.hadRelease || p.Kind != plan.Create && !r.hadRelease {
		return errors.New("managed release drift")
	}
	if r.hadRelease {
		if x.previous.Managed == nil || e.Plans == nil {
			return errors.New("managed previous release required")
		}
		r.previousPlan, x.previousDesired, err = e.Plans.LoadPlan(ctx, x.previous.PlanID)
		if err != nil {
			return err
		}
		x.hasPrevious = x.previous.Managed.Frontend != nil
		r.previousFrontendRunning = x.hasPrevious && facts.Input.Managed != nil && facts.Input.Managed.Intent != nil && facts.Input.Managed.Intent.Current && facts.Input.Managed.Intent.FrontendRunning
	}
	if p.Managed.Frontend != nil || x.hasPrevious {
		r.previousFrontendPermit, err = e.Managed.FrontendPermitOwner(ctx, p.App)
		if err != nil {
			return err
		}
	}
	r.engineService, err = systemd.ParseUnit("brine-engine-" + string(p.Managed.Database.BindingID) + ".service")
	if err != nil {
		return err
	}
	x.service, err = systemd.ParseUnit(p.App + ".service")
	if err != nil {
		return err
	}
	if p.Managed.Frontend != nil && (e.Podman == nil || e.Units == nil || e.Health == nil) {
		return errors.New("managed frontend adapters required")
	}
	if x.hasPrevious && p.Managed.Frontend == nil {
		if e.Units == nil || e.Podman == nil {
			return errors.New("managed old frontend adapters required")
		}
		if _, ok := e.Units.(ManagedFrontendUnits); !ok {
			return errors.New("retained frontend removal required")
		}
	}
	return nil
}
