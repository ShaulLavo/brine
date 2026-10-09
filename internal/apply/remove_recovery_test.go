package apply

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

func applyRemoveBoundary(t *testing.T, r *rig, name string) {
	t.Helper()
	ctx := context.Background()
	r.intent = name
	switch name {
	case "preflight":
	case "withdraw_route":
		_, err := r.Withdraw(ctx, removalRouting(r.plan, r.facts), r.plan.App)
		if err != nil {
			t.Fatal(err)
		}
	case "stop_unit":
		r.active = false
	case "remove_unit":
		if err := r.Remove(ctx, r.plan.Removal.Units[0].Name, r.plan.Removal.Units[0].Hash); err != nil {
			t.Fatal(err)
		}
	case "reload_units":
	case "retire_app":
		if err := r.RetireApp(ctx, "operation", r.plan.App, r.plan.Removal.ReleaseID); err != nil {
			t.Fatal(err)
		}
	}
}
func TestRemoveRecoveryEachEffectBoundary(t *testing.T) {
	for index, name := range removeSteps {
		for _, point := range []string{"before", "after", "journaled"} {
			t.Run(name+"/"+point, func(t *testing.T) {
				r := newRemoveRig(t)
				for _, earlier := range removeSteps[:index] {
					applyRemoveBoundary(t, r, earlier)
				}
				events := recoveryEvents(removeSteps[:index+1]...)
				if point != "before" {
					applyRemoveBoundary(t, r, name)
				}
				if point == "journaled" {
					events[len(events)-1].Payload, _ = json.Marshal(ops.StepPayload{Step: name, Outcome: "completed"})
				}
				r.events = events
				r.effects = nil
				states := map[string]State{"preflight": Preflight, "withdraw_route": Preparing, "stop_unit": Quiescing, "remove_unit": Starting, "reload_units": Checking, "retire_app": Committing}
				r.state = states[name]
				op := Operation{ID: "operation", Kind: ops.Deploy, PlanID: r.plan.Hash, State: r.state}
				assessment, err := r.executor.InspectRecovery(context.Background(), op, r.plan, r.desired, events)
				if err != nil || assessment.Action != ResumeForward && assessment.Action != FinishSucceeded {
					t.Fatalf("%+v %v", assessment, err)
				}
				if len(r.effects) != 0 {
					t.Fatal("inspection mutated")
				}
				if err = r.executor.Recover(context.Background(), assessment); err != nil || r.state != Succeeded || !r.committed || r.hasRelease {
					t.Fatal(err, r.state, r.effects)
				}
				if err = r.executor.Recover(context.Background(), assessment); err == nil {
					t.Fatal("reused recovery")
				}
			})
		}
	}
}
func TestRemoveRecoveryDriftAndUnknownWriterFailClosed(t *testing.T) {
	for _, drift := range []string{"route", "unit", "generation", "policy", "writer", "job", "extra_unit"} {
		t.Run(drift, func(t *testing.T) {
			r := newRemoveRig(t)
			applyRemoveBoundary(t, r, "withdraw_route")
			applyRemoveBoundary(t, r, "stop_unit")
			r.state = Starting
			events := recoveryEvents("preflight", "withdraw_route", "stop_unit", "remove_unit")
			switch drift {
			case "route":
				r.facts.Input.Snapshot.CaddyConfig.Value.Files = append(r.facts.Input.Snapshot.CaddyConfig.Value.Files, target.CaddyFile{Name: "foreign.caddy", Hash: r.plan.Removal.Route.Hash})
			case "extra_unit":
				unit := target.Unit{Name: "hello.volume", Hash: r.plan.Removal.Route.Hash}
				(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits.Value = &[]target.Unit{r.plan.Removal.Units[0], unit}
			case "unit":
				(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits.Value = &[]target.Unit{{Name: "hello.container", Hash: r.plan.Removal.Route.Hash}}
			case "generation":
				r.facts.Input.State.Generation++
			case "policy":
				r.facts.Input.Desired.PolicyHash = r.plan.Removal.Route.Hash
			case "writer":
				r.active = true
			case "job":
				r.executor.Systemd.(*systemd.Fake).JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return true, nil }
				r.executor.EffectTimeout = 1
			}
			assessment, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation", Kind: ops.Deploy, PlanID: r.plan.Hash, State: r.state}, r.plan, r.desired, events)
			if err != nil || assessment.Action != RequireRecovery {
				t.Fatal(assessment, err)
			}
		})
	}
}

func TestInterruptedNoOpRemovalCompletesAfterAbsenceValidation(t *testing.T) {
	for _, outcome := range []string{"intent", "completed"} {
		t.Run(outcome, func(t *testing.T) {
			r := newRig(t, false)
			var err error
			r.plan, err = plan.BuildRemove(r.facts.Input)
			if err != nil || r.plan.Kind != plan.NoOp {
				t.Fatal(r.plan, err)
			}
			r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
			r.executor.Units = nil
			r.executor.Routes = nil
			r.executor.Systemd = nil
			r.executor.Podman = nil
			r.state = Preflight
			raw, _ := json.Marshal(ops.StepPayload{Step: "preflight", Outcome: outcome})
			events := []Event{{Kind: "step", Payload: raw}}
			r.events = events
			op := Operation{ID: "operation", Kind: ops.Deploy, PlanID: r.plan.Hash, State: Preflight}
			assessment, err := r.executor.InspectRecovery(context.Background(), op, r.plan, r.desired, events)
			if err != nil || assessment.Action != FinishSucceeded {
				t.Fatal(assessment, err)
			}
			if len(r.effects) != 0 || len(r.events) != 1 {
				t.Fatal("inspection changed state", r.effects, r.events)
			}
			if err = r.executor.Recover(context.Background(), assessment); err != nil || r.state != Succeeded || len(r.effects) != 0 {
				t.Fatal(err, r.state, r.effects)
			}
			if err = r.executor.Recover(context.Background(), assessment); err == nil {
				t.Fatal("reused assessment")
			}
		})
	}
}

func TestInterruptedNoOpRemovalRefusesChangedAbsenceEvidence(t *testing.T) {
	for _, change := range []string{"app", "route", "generation", "policy", "unknown_apps", "unknown_route", "effect_event"} {
		t.Run(change, func(t *testing.T) {
			r := newRig(t, false)
			var err error
			r.plan, err = plan.BuildRemove(r.facts.Input)
			if err != nil {
				t.Fatal(err)
			}
			r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
			raw, _ := json.Marshal(ops.StepPayload{Step: "preflight", Outcome: "completed"})
			events := []Event{{Kind: "step", Payload: raw}}
			switch change {
			case "app":
				r.facts.Input.Snapshot.Apps = target.Known([]target.App{{Name: r.plan.App, QuadletUnits: target.Known([]target.Unit{})}})
			case "route":
				r.facts.Input.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: 1, Files: []target.CaddyFile{{Name: r.plan.App + ".caddy", Hash: r.plan.Hash}}})
			case "generation":
				r.facts.Input.State.Generation++
				r.facts.Input.Snapshot.Generation = target.Known(r.facts.Input.State.Generation)
			case "policy":
				r.facts.Input.Desired.PolicyHash = r.plan.Hash
			case "unknown_apps":
				r.facts.Input.Snapshot.Apps = target.Observation[[]target.App]{Status: target.Unknown}
			case "unknown_route":
				r.facts.Input.Snapshot.CaddyConfig = target.Observation[target.CaddyConfigSet]{Status: target.Unknown}
			case "effect_event":
				events = recoveryEvents("preflight", "withdraw_route")
			}
			assessment, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation", Kind: ops.Deploy, PlanID: r.plan.Hash, State: Preflight}, r.plan, r.desired, events)
			if err != nil || assessment.Action != RequireRecovery || len(r.effects) != 0 || len(r.events) != 0 {
				t.Fatal(assessment, err, r.events, r.effects)
			}
		})
	}
}
