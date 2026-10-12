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

func (run *managedExecution) preflight(ctx context.Context) error {
	x := run.x
	e, p, d := x.executor, x.plan, x.desired

	if managedChange(p, plan.RemoveManagedFrontend) {
		return errors.New("managed frontend removal not yet admitted")
	}
	if e.Managed == nil || e.Facts == nil || e.Releases == nil || e.Systemd == nil || e.Routes == nil || p.Managed == nil || !p.Managed.Valid() || p.Managed.State != data.EngineInitialized || !desiredMatches(p, d) || p.Kind != plan.Create && p.Kind != plan.Update && p.Kind != plan.NoOp || len(p.Conflicts) != 0 {
		return errors.New("managed deployment input required")
	}
	facts, err := e.Facts.Read(ctx)
	if err != nil {
		return err
	}
	fresh, err := plan.Build(facts.Input)
	if p.Lifecycle != "" {
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
	x.previous, run.hadRelease, err = e.Releases.CurrentRelease(ctx, p.App)
	if err != nil {
		return err
	}
	if p.Kind == plan.Create && run.hadRelease || p.Kind != plan.Create && !run.hadRelease {
		return errors.New("managed release drift")
	}
	if run.hadRelease {
		if x.previous.Managed == nil || e.Plans == nil {
			return errors.New("managed previous release required")
		}
		run.previousPlan, x.previousDesired, err = e.Plans.LoadPlan(ctx, x.previous.PlanID)
		if err != nil {
			return err
		}
		x.hasPrevious = x.previous.Managed.Frontend != nil
	}
	if p.Managed.Frontend != nil {
		run.previousFrontendPermit, err = e.Managed.FrontendPermitOwner(ctx, p.App)
		if err != nil {
			return err
		}
	}
	run.engineService, err = systemd.ParseUnit("brine-engine-" + string(p.Managed.Database.BindingID) + ".service")
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
	return nil
}
