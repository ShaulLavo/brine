package apply

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

type missingManagedRouteReceipt struct {
	*managedRuntimeFake
	missing bool
}

func (f *missingManagedRouteReceipt) CompletedManaged(ctx context.Context, owner string, p plan.Plan, d policy.Desired) error {
	if f.missing {
		return injected
	}
	return f.managedRuntimeFake.CompletedManaged(ctx, owner, p, d)
}
func (f *missingManagedRouteReceipt) UnrecordedManagedRoute(context.Context, string, plan.Plan, policy.Desired) error {
	return f.call("managed_receipt_observed")
}
func (f *missingManagedRouteReceipt) RecordRecoveredManagedRoute(_ context.Context, resolver, owner string, p plan.Plan, _ policy.Desired, route caddy.State) error {
	if resolver != "resolve" || owner != "op" || !f.missing || p.Hash != f.rig.plan.Hash || !reflect.DeepEqual(route, f.rig.facts.Routing) {
		return injected
	}
	if err := f.call("managed_receipt_recorded"); err != nil {
		return err
	}
	f.missing = false
	f.rig.facts.Input.State.Generation++
	f.rig.effects = append(f.rig.effects, "receipt")
	return nil
}

func missingManagedRouteReceiptFixture(t *testing.T) (*managedRouteRecoveryRig, Operation, Operation, *missingManagedRouteReceipt) {
	t.Helper()
	r, source, resolver := managedRouteRecoveryFixture(t)
	for i, event := range r.events {
		var step ops.StepPayload
		if event.Kind != "step" || json.Unmarshal(event.Payload, &step) != nil || step.Step != "managed_route_receipt" || step.Outcome != "completed" {
			continue
		}
		step.Outcome = "unknown"
		r.events[i].Payload, _ = json.Marshal(step)
		r.events = r.events[:i+1]
		break
	}
	f := &missingManagedRouteReceipt{managedRuntimeFake: r.executor.Managed.(*managedRuntimeFake), missing: true}
	r.executor.Managed = f
	r.facts.Input.State.Generation--
	return r, source, resolver, f
}

func TestManagedRouteReceiptResolutionRecordsOnlyObservedMetadata(t *testing.T) {
	r, source, resolver, runtime := missingManagedRouteReceiptFixture(t)
	original := append([]Event(nil), r.events...)
	assessment, err := r.executor.InspectResolution(t.Context(), resolver, source, r.plan, r.desired, r.events)
	if err != nil || assessment.Action != ResumeForward || assessment.Step != "managed_route_receipt" || len(r.effects) != 0 || !runtime.missing {
		t.Fatal("missing receipt not observed without effects", err, assessment.Action, assessment.Step, r.effects)
	}
	if err := r.executor.Recover(t.Context(), assessment); err != nil {
		t.Fatal(err)
	}
	if runtime.missing || r.state != Succeeded || !r.committed || r.release.ID != source.ID || !reflect.DeepEqual(r.effects, []string{"receipt", "commit"}) || !reflect.DeepEqual(r.checks, []bool{false, true}) {
		t.Fatal("receipt recovery replayed deployment", runtime.missing, r.state, r.effects, r.checks)
	}
	if !reflect.DeepEqual(original, r.events[:len(original)]) {
		t.Fatal("original operation prefix changed")
	}
}

func TestManagedRouteReceiptResolutionRefusesChangedEvidenceBeforeMetadata(t *testing.T) {
	for _, change := range []string{"native", "route", "stale-assessment"} {
		t.Run(change, func(t *testing.T) {
			r, source, resolver, runtime := missingManagedRouteReceiptFixture(t)
			if change == "native" {
				runtime.failure = "managed_receipt_observed"
			}
			if change == "route" {
				r.loadedFailure = true
			}
			assessment, err := r.executor.InspectResolution(t.Context(), resolver, source, r.plan, r.desired, r.events)
			if err != nil {
				t.Fatal(err)
			}
			if change == "stale-assessment" {
				r.facts.Input.State.Generation++
				if assessment.Action != ResumeForward || r.executor.Recover(t.Context(), assessment) == nil {
					t.Fatal("stale assessment repaired metadata")
				}
			} else if assessment.Action != RequireRecovery {
				t.Fatal("changed evidence admitted", assessment.Action)
			}
			if !runtime.missing || r.committed || len(r.effects) != 0 {
				t.Fatal("refused recovery changed metadata", r.effects)
			}
		})
	}
}

func TestManagedRouteReceiptResolutionAdoptsAlreadyRecordedMetadata(t *testing.T) {
	r, source, resolver, runtime := missingManagedRouteReceiptFixture(t)
	runtime.missing = false
	r.facts.Input.State.Generation++
	assessment, err := r.executor.InspectResolution(t.Context(), resolver, source, r.plan, r.desired, r.events)
	if err != nil || assessment.Action != ResumeForward {
		t.Fatal(err, assessment.Action)
	}
	if err := r.executor.Recover(t.Context(), assessment); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.effects, []string{"commit"}) {
		t.Fatal("recorded receipt repeated", r.effects)
	}
}
