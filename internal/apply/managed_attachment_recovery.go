package apply

import (
	"context"
	"errors"
	"reflect"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type managedAttachmentRecovery interface {
	InterruptedFrontend(context.Context, string, plan.Plan, policy.Desired) error
}

func (e *Executor) inspectManagedAttachment(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, events []Event, r Recovery) (Recovery, error) {
	if len(r.resolutionOwners) == 0 || !ops.InterruptedFrontendAttachment(events, p) || e.Plans == nil || e.Units == nil || e.Systemd == nil {
		return r, nil
	}
	inspector, ok := e.Managed.(managedAttachmentRecovery)
	if !ok {
		return r, nil
	}
	owner := r.resolutionOwners[len(r.resolutionOwners)-1]
	proof, cancel := context.WithTimeout(ctx, e.effectTimeout())
	defer cancel()
	facts, err := e.Facts.Read(proof)
	if err != nil {
		return r, err
	}
	if facts.Input.Desired.PolicyHash != p.PolicyHash || facts.Input.Desired.PolicyVersion != p.PolicyVersion || !reflect.DeepEqual(facts.Input.State.Target, p.Target) || p.ObservedGeneration.Status != target.KnownStatus || p.ObservedGeneration.Value == nil || facts.Input.State.Generation != *p.ObservedGeneration.Value+1 {
		return r, nil
	}
	previous, exists, err := e.Releases.CurrentRelease(proof, p.App)
	if err != nil || !exists || previous.Managed == nil || previous.Managed.Frontend != nil || previous.Managed.BindingID != p.Managed.Database.BindingID {
		return r, err
	}
	previousPlan, previousDesired, err := e.Plans.LoadPlan(proof, previous.PlanID)
	if err != nil || previousPlan.Managed == nil || previousPlan.Managed.Database != p.Managed.Database {
		return r, err
	}
	if p.Image.ManifestDigest.Value == nil {
		return r, nil
	}
	unit, err := quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
	if err != nil || inspector.InterruptedFrontend(proof, owner, p, d) != nil {
		return r, nil
	}
	service, err := systemd.ParseUnit(p.App + ".service")
	if err != nil {
		return r, err
	}
	x := &execution{executor: e, id: op.ID, plan: p, desired: d, state: op.State, facts: facts, previous: previous, previousDesired: previousDesired, unit: unit, service: service, installed: true, started: true, managedAttachmentOwner: owner}
	r.Action, r.Step, r.execution = RestorePrevious, "frontend_check", x
	return r, nil
}

func (x *execution) rollbackManagedAttachment(ctx context.Context) error {
	previousPlan, _, err := x.executor.Plans.LoadPlan(ctx, x.previous.PlanID)
	if err != nil {
		return err
	}
	recovery := managedExecution{x: x, hadRelease: true, previousPlan: previousPlan, frontendBound: true, intentWritten: true}
	return recovery.compensate(ctx, &Error{Step: "frontend_check", Code: "health_failed", Cause: errors.New("interrupted frontend attachment")})
}
