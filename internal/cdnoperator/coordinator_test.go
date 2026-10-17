package cdnoperator

import (
	"context"
	"errors"
	"testing"

	"github.com/ShaulLavo/brine/internal/cdn"
)

type bindingHost struct {
	Host
	initial Progress
	next    Progress
}

func (h bindingHost) Apply(context.Context, ApplyRequest) (Progress, error) {
	return h.initial, nil
}

func (h bindingHost) Next(context.Context, OperationRequest) (Progress, error) {
	return h.initial, nil
}

func (h bindingHost) RecordResult(context.Context, ResultRequest) (Progress, error) {
	return h.next, nil
}

type bindingEdge struct {
	cdn.EdgeDriver
	writes int
}

func (d *bindingEdge) Execute(_ context.Context, p cdn.Permit) (cdn.EffectResult, error) {
	d.writes++
	return cdn.Uncertain{Effect: p.Effect, Code: "unsettled"}, nil
}

type bindingDrivers struct {
	Drivers
	edge *bindingEdge
}

func (d bindingDrivers) Edge(cdn.ProfileScope) (cdn.EdgeDriver, error) { return d.edge, nil }

func bindingProgress(t *testing.T, plan cdn.Plan, operation cdn.OperationID) Progress {
	t.Helper()
	step := plan.Steps[0]
	before := step.Precondition.(cdn.InitialObservation)
	permit, err := plan.NewPermit(operation, 0, cdn.Action{Kind: step.Kind, Attachment: plan.Context.Attachment,
		Profile: step.Profile, Resource: before.Resource, Expected: before.Before, Desired: step.Desired})
	if err != nil {
		t.Fatal(err)
	}
	permit.Claim = "claim"
	progress := Progress{Operation: operation, Plan: plan, Work: cdn.NeedEffect{Permit: permit}}
	if err := progress.Validate(); err != nil {
		t.Fatal(err)
	}
	return progress
}

func anotherBindingPlan(t *testing.T, plan cdn.Plan) cdn.Plan {
	t.Helper()
	context := plan.Context
	context.PolicyHash = cdn.Hash([]byte("another-policy"))
	other, err := cdn.NewPlan(context, plan.Sample, plan.Steps, nil)
	if err != nil || other.ID == plan.ID {
		t.Fatal("could not construct distinct valid plan", err)
	}
	return other
}

func TestCDNCoordinatorBindsAdmissionToReviewedPlan(t *testing.T) {
	plan := operatorPlan(t)
	edge := &bindingEdge{}
	foreign := bindingProgress(t, anotherBindingPlan(t, plan), "operation")
	c := Coordinator{Host: bindingHost{initial: foreign}, Drivers: bindingDrivers{edge: edge}}
	progress, err := c.Apply(t.Context(), ApplyRequest{PlanID: plan.ID, IdempotencyKey: "apply"})
	if !errors.Is(err, cdn.ErrConflict) || edge.writes != 0 || progress.Operation != "" {
		t.Fatalf("foreign admission reached provider or escaped as accepted: writes=%d operation=%s err=%v", edge.writes, progress.Operation, err)
	}
}

func TestCDNCoordinatorBindsResumeToRequestedOperation(t *testing.T) {
	plan := operatorPlan(t)
	edge := &bindingEdge{}
	c := Coordinator{Host: bindingHost{initial: bindingProgress(t, plan, "foreign")}, Drivers: bindingDrivers{edge: edge}}
	progress, err := c.Resume(t.Context(), "requested")
	if !errors.Is(err, cdn.ErrConflict) || edge.writes != 0 || progress.Operation != "" {
		t.Fatalf("foreign resume reached provider or escaped as accepted: writes=%d operation=%s err=%v", edge.writes, progress.Operation, err)
	}
}

func TestCDNCoordinatorPreservesIdentityAcrossResponses(t *testing.T) {
	plan := operatorPlan(t)
	initial := bindingProgress(t, plan, "requested")
	foreign := []Progress{bindingProgress(t, plan, "foreign"), bindingProgress(t, anotherBindingPlan(t, plan), "requested")}
	for _, response := range foreign {
		edge := &bindingEdge{}
		c := Coordinator{Host: bindingHost{initial: initial, next: response}, Drivers: bindingDrivers{edge: edge}}
		progress, err := c.Resume(t.Context(), initial.Operation)
		if !errors.Is(err, cdn.ErrConflict) || edge.writes != 1 || progress.Operation != initial.Operation || progress.Plan.ID != initial.Plan.ID {
			t.Fatalf("foreign transition changed accepted work: writes=%d operation=%s plan=%s err=%v", edge.writes, progress.Operation, progress.Plan.ID, err)
		}
	}
}
