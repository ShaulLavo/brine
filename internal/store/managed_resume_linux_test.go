//go:build linux

package store

import (
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
	"testing"
)

func TestManagedResumeUsesNewLifecycleOwnerAfterAbandonment(t *testing.T) {
	fixture := newNativeAbandonFixture(t, true)
	cap, observed := fixture.open(t)
	if _, err := fixture.state.CompleteNativeAbandonment(t.Context(), cap, observed); err != nil {
		t.Fatal(err)
	}
	if err := cap.Close(); err != nil {
		t.Fatal(err)
	}
	state := fixture.state
	stopped, err := state.ReadManagedDesired(t.Context(), fixture.point.BindingID)
	if err != nil || stopped.EngineRunning || stopped.FrontendRunning || stopped.IngressServing {
		t.Fatal("abandonment retained live desired authority", err)
	}
	release, err := state.CurrentRelease(t.Context(), fixture.operation.App)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := state.LoadPlan(t.Context(), release.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	native, err := state.ReadEnginePermit(t.Context(), fixture.point.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	input := managedFreshInput(t, state, original, native.Runtime)
	input.Managed.NativeState = target.Known("stopped")
	input.Managed.OwnedNativeState = target.Known("stopped")
	planned, err := plan.BuildLifecycle(input, plan.StartApp)
	if err != nil || planned.Kind == plan.Conflict {
		t.Fatal(err, planned.Conflicts)
	}
	if !planned.ManagedLifecycle.Engine || !planned.ManagedLifecycle.Frontend {
		t.Fatal("resume did not select both stopped components")
	}
	if _, err := state.SavePlan(t.Context(), planned, input.Desired); err != nil {
		t.Fatal(err)
	}
	op, _, err := state.CreateOperation(t.Context(), ops.Intent{Kind: ops.Deploy, PlanID: planned.Hash}, fixture.operation.Requester, "reviewed-public-resume")
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []ops.State{ops.Preflight, ops.Preparing} {
		if err := state.SetOperationState(t.Context(), op.ID, phase); err != nil {
			t.Fatal(err)
		}
	}
	desired := managedDesiredResumeFixture(planned, op.ID)
	if _, err := state.CommitManagedDesired(t.Context(), op.ID, planned.ManagedIntentRevision, desired); err != nil {
		t.Fatal(err)
	}
	if err := state.SetOperationState(t.Context(), op.ID, ops.Starting); err != nil {
		t.Fatal(err)
	}
	permission, err := state.ReadManagedFrontendPermit(t.Context(), planned.App)
	if err != nil || !permission.Pending || permission.Permit.OperationID != op.ID || permission.Permit.PlanID != planned.Hash || permission.Permit.UnitHash != release.Managed.Frontend.Unit.Hash {
		t.Fatal("owned current frontend did not adopt reviewed lifecycle scope", err)
	}
	current, err := state.CurrentRelease(t.Context(), planned.App)
	if err != nil || current.ID != release.ID || current.PlanID != release.PlanID {
		t.Fatal("lifecycle replaced retained release", err)
	}
	finalNative, err := state.ReadEnginePermit(t.Context(), fixture.point.BindingID)
	if err != nil || string(abandonJSON(t, finalNative.Runtime)) != string(abandonJSON(t, native.Runtime)) || finalNative.Runtime.EpochID != fixture.point.EpochID {
		t.Fatal("resume changed native runtime or epoch", err)
	}
	point, err := state.ReadNativePreparedPoint(t.Context(), fixture.point.PointID)
	if err != nil || point.SourceSealSHA256 != fixture.point.SourceSealSHA256 || point.PreparationOperationID != fixture.point.PreparationOperationID {
		t.Fatal("resume changed original retained point", err)
	}
}

func managedDesiredResumeFixture(p plan.Plan, operation string) ManagedDesiredState {
	return ManagedDesiredState{BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, Revision: p.ManagedIntentRevision + 1, PlanID: p.Hash, OperationID: operation, EngineRunning: true, FrontendRunning: true, IngressServing: true}
}
