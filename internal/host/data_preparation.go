package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
)

type preparationInspector interface {
	InspectPreparationRoots(context.Context, policy.Desired) ([]data.RootEvidence, error)
}

func (s Service) preparationFacts(ctx context.Context, app spec.App) (apply.Facts, []data.RootEvidence, error) {
	inspector, ok := s.Data.(preparationInspector)
	if !ok {
		return apply.Facts{}, nil, result.New(result.DependencyMissing, nil)
	}
	reader := s
	reader.Data = nil
	facts, err := reader.facts(ctx, app)
	if err != nil {
		return facts, nil, err
	}
	roots, err := inspector.InspectPreparationRoots(ctx, facts.Input.Desired)
	return facts, roots, err
}

// PlanDataPreparation freezes proposals only. SavePlan is the sole planning
// write: no reservation, data directory, schema registry, or allocation receipt.
func (s Service) PlanDataPreparation(ctx context.Context, app spec.App) (_ dispatch.Planned, retErr error) {
	if s.Requester == "" || s.Store == nil {
		return dispatch.Planned{}, result.New(result.DispatchOperationRefused, nil)
	}
	lock, err := s.Store.AcquireHostLock(ctx)
	if err != nil {
		return dispatch.Planned{}, err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	facts, roots, err := s.preparationFacts(ctx, app)
	if err != nil {
		return dispatch.Planned{}, err
	}
	d := facts.Input.Desired
	incarnation, err := s.Store.ActiveDataIncarnation(ctx, string(d.Name))
	freshIncarnation := errors.Is(err, store.ErrNotFound)
	if errors.Is(err, store.ErrNotFound) {
		id, e := data.NewID()
		if e != nil {
			return dispatch.Planned{}, e
		}
		incarnation = data.AppIncarnationID(id)
	} else if err != nil {
		return dispatch.Planned{}, err
	} else {
		scopes, e := s.Store.ReadCredentialScopes(ctx, string(d.Name))
		if e != nil || len(scopes) != len(d.Databases) {
			return dispatch.Planned{}, result.New(result.Conflict, e)
		}
	}
	proposals := make([]data.AllocationProposal, 0, len(d.Databases))
	for _, declaration := range d.Databases {
		var destination data.Destination
		for _, candidate := range d.BackupDestinations {
			if candidate.Reference == declaration.BackupDestination {
				destination = candidate
			}
		}
		existing, e := s.Store.ExistingDatabase(ctx, store.DataReservation{App: string(d.Name), Database: declaration, Destination: destination})
		var proposal data.AllocationProposal
		if errors.Is(e, store.ErrNotFound) {
			if !freshIncarnation {
				return dispatch.Planned{}, result.New(result.Conflict, e)
			}
			proposal, e = data.NewAllocationProposal(declaration, destination, incarnation)
		} else if e == nil {
			proposal = data.AllocationProposal{Database: existing.Database, Replica: existing.Replica}
		}
		if e != nil {
			return dispatch.Planned{}, e
		}
		proposals = append(proposals, proposal)
	}
	p, err := plan.BuildDataPreparation(facts.Input, proposals, roots)
	if err != nil {
		return dispatch.Planned{}, err
	}
	id, err := s.Store.SavePlan(ctx, p, d)
	return dispatch.Planned{PlanID: id, Kind: p.Kind, Diff: p.Diff, Conflicts: p.Conflicts}, err
}

// runDataPreparation executes only the approved allocation effect. It has no
// release, unit, writer, replica activation, or routing effects to compensate.
func (e Executor) runDataPreparation(ctx context.Context, id string, p plan.Plan, d policy.Desired) error {
	state := e.Service.Store
	if state == nil || p.Kind != plan.Create || len(p.Conflicts) > 0 {
		return result.New(result.Conflict, nil)
	}
	operation, err := state.GetOperation(ctx, store.OpID(id))
	if err != nil {
		return err
	}
	if operation.Kind != ops.Deploy || operation.PlanID != p.Hash || operation.App != p.App || (operation.State != ops.Queued && operation.State != ops.LaunchUnknown) {
		return result.New(result.Conflict, nil)
	}
	if err = state.SetOperationState(ctx, store.OpID(id), ops.Preflight); err != nil {
		return err
	}
	fail := func(cause error) error {
		return errors.Join(cause, state.SetOperationState(context.WithoutCancel(ctx), store.OpID(id), ops.Failed))
	}
	facts, roots, err := e.Service.preparationFacts(ctx, App(d))
	if err != nil {
		return fail(err)
	}
	fresh, err := plan.BuildDataPreparation(facts.Input, p.DataAllocations, roots)
	if err != nil {
		return fail(err)
	}
	approved, err := p.CanonicalBytes()
	if err != nil {
		return fail(err)
	}
	rebuilt, err := fresh.CanonicalBytes()
	if err != nil || !bytes.Equal(approved, rebuilt) {
		return fail(result.New(result.Conflict, errors.Join(err, ops.RecordPlanDrift(ctx, state, id, p, fresh))))
	}
	if e.Engine.PersistentData == nil {
		return fail(result.New(result.DependencyMissing, nil))
	}
	journal := func(outcome string) error {
		payload, mErr := json.Marshal(ops.StepPayload{Step: "prepare_data", Outcome: outcome})
		if mErr != nil {
			return mErr
		}
		_, mErr = state.AppendEvent(context.WithoutCancel(ctx), store.OpID(id), ops.Event{Kind: "step", Payload: payload})
		return mErr
	}
	if err = state.SetOperationState(ctx, store.OpID(id), ops.Preparing); err != nil {
		return err
	}
	if err = journal("intent"); err != nil {
		return errors.Join(err, state.SetOperationState(context.WithoutCancel(ctx), store.OpID(id), ops.RecoveryRequired))
	}
	effectErr := e.Engine.PersistentData.PreparePersistent(ctx, id, p, d)
	complete, readErr := e.Engine.PersistentData.PersistentPrepared(context.WithoutCancel(ctx), id, p, d)
	if !complete || readErr != nil {
		return errors.Join(effectErr, readErr, journal("unknown"), state.SetOperationState(context.WithoutCancel(ctx), store.OpID(id), ops.RecoveryRequired), result.New(result.RecoveryRequired, nil))
	}
	if err = journal("completed"); err != nil {
		return errors.Join(err, state.SetOperationState(context.WithoutCancel(ctx), store.OpID(id), ops.RecoveryRequired))
	}
	return state.SetOperationState(context.WithoutCancel(ctx), store.OpID(id), ops.Succeeded)
}
