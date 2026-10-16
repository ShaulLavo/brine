package apply

import (
	"context"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

type attachmentRuntime struct{ *managedRuntimeFake }

func (r attachmentRuntime) InterruptedFrontend(context.Context, string, plan.Plan, policy.Desired) error {
	return r.call("interrupted_frontend")
}

func TestManagedInterruptedAttachmentResolvesBackToEngineOnlyRelease(t *testing.T) {
	r, runtime := managedLifecycleRig(t, false, plan.StartApp)
	front := managedInitializedRig(t, true)
	r.desired, r.facts.Input.Desired = front.desired, front.desired
	r.facts.Input.Image = front.facts.Input.Image
	p, err := plan.Build(r.facts.Input)
	if err != nil || p.Kind != plan.Update || p.Managed.Frontend == nil {
		t.Fatal(err, p.Conflicts)
	}
	r.plan = p
	r.executor.Managed = attachmentRuntime{runtime}
	r.unknownStep = "frontend_check"
	if err := r.executor.Run(context.Background(), "attach", p, r.desired); err == nil || r.state != RecoveryRequired {
		t.Fatal(err, r.state)
	}
	r.unknownStep = ""
	r.active = false
	r.facts.Input.State.Generation = *p.ObservedGeneration.Value + 1
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	source := Operation{ID: "attach", PlanID: p.Hash, Kind: ops.Deploy, App: p.App, State: RecoveryRequired}
	resolver := Operation{ID: "resolve", PlanID: p.Hash, Kind: ops.Resolve, App: p.App, State: Queued, RecoveryOf: source.ID}
	r.state = Queued
	original := append([]Event(nil), r.events...)
	assessment, err := r.executor.InspectResolution(context.Background(), resolver, source, p, r.desired, r.events)
	if err != nil || assessment.Action != RestorePrevious {
		t.Fatalf("attachment remains stuck: %s %s %v", assessment.Action, assessment.Step, err)
	}
	r.facts.Input.State.Generation++
	stale, err := r.executor.InspectResolution(context.Background(), resolver, source, p, r.desired, r.events)
	if err != nil || stale.Action != RequireRecovery {
		t.Fatal("changed generation authorized compensation", err, stale.Action)
	}
	r.facts.Input.State.Generation--
	runtime.failure = "interrupted_frontend"
	changed, err := r.executor.InspectResolution(context.Background(), resolver, source, p, r.desired, r.events)
	if err != nil || changed.Action != RequireRecovery {
		t.Fatal("changed frontend authorized compensation", err, changed.Action)
	}
	runtime.failure = ""
	if err := r.executor.Recover(context.Background(), assessment); err == nil && r.state != RolledBack {
		t.Fatal(err, r.state)
	}
	if r.state != RolledBack || r.release.Managed.Frontend != nil || r.committed || r.active {
		t.Fatal("engine-only release not restored", r.state)
	}
	if !reflect.DeepEqual(original, r.events[:len(original)]) {
		t.Fatal("source journal changed")
	}
}
