package apply

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

type managedRouteRecoveryRig struct {
	*rig
	checks        []bool
	owners        []string
	loadedFailure bool
	checkFailure  bool
	afterHealth   func()
}

func (r *managedRouteRecoveryRig) Check(_ context.Context, _ policy.Desired, _ target.Port, routed bool) error {
	r.checks = append(r.checks, routed)
	if r.checkFailure {
		return injected
	}
	if routed && r.afterHealth != nil {
		r.afterHealth()
	}
	return nil
}
func (r *managedRouteRecoveryRig) InspectManagedLoaded(_ context.Context, expected caddy.State, _ plan.Plan, _ policy.Desired) error {
	if r.loadedFailure || !reflect.DeepEqual(expected, r.facts.Routing) {
		return injected
	}
	return nil
}
func (r *managedRouteRecoveryRig) CommitManagedResolutionRelease(ctx context.Context, resolver string, generation uint64, release Release) error {
	if resolver != "resolve" || generation != r.facts.Input.State.Generation {
		return injected
	}
	r.owners = append(r.owners, release.ID)
	return r.CommitRelease(ctx, r.plan.App, release)
}
func managedRouteRecoveryFixture(t *testing.T) (*managedRouteRecoveryRig, Operation, Operation) {
	t.Helper()
	r := managedInitializedRig(t, true)
	runtime := &managedRuntimeFake{rig: r}
	r.executor.Managed = runtime
	r.unknownStep = "managed_route_check"
	if err := r.executor.Run(context.Background(), "op", r.plan, r.desired); err == nil || r.committed || r.state != RecoveryRequired {
		t.Fatal("unknown route check fixture", err, r.state)
	}
	r.unknownStep = ""
	r.facts.Routing = caddy.State{Generation: 1, Files: map[string]string{r.plan.App + ".caddy": "sha256:" + strings.Repeat("e", 64)}}
	r.facts.Input.State.Generation = *r.plan.ObservedGeneration.Value + 2
	apps := r.facts.Input.Snapshot.Apps.Value
	(*apps)[0].Image = target.Known(target.Image{Digest: r.plan.Image.Digest, Platform: r.plan.Image.Platform})
	(*apps)[0].AllocatedHostPort = target.Known(r.plan.HostPort)
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	wrapped := &managedRouteRecoveryRig{rig: r}
	r.executor.Routes, r.executor.Releases, r.executor.Health = wrapped, wrapped, wrapped
	r.effects = nil
	runtime.calls = nil
	source := Operation{ID: "op", PlanID: r.plan.Hash, Kind: ops.Deploy, App: r.plan.App, State: RecoveryRequired}
	resolver := Operation{ID: "resolve", PlanID: r.plan.Hash, Kind: ops.Resolve, App: r.plan.App, RecoveryOf: source.ID, State: Queued}
	r.state = Queued
	return wrapped, source, resolver
}
func TestManagedRouteResolutionOnlyChecksAndCommitsOriginalArtifacts(t *testing.T) {
	r, source, resolver := managedRouteRecoveryFixture(t)
	original := append([]Event(nil), r.events...)
	assessment, err := r.executor.InspectResolution(context.Background(), resolver, source, r.plan, r.desired, r.events)
	if err != nil || assessment.Action != ResumeForward || assessment.Step != "managed_route_check" {
		t.Fatal(assessment.Action, assessment.Step, err)
	}
	if len(r.effects) != 0 || len(r.checks) != 0 || r.committed {
		t.Fatal("inspection performed effects")
	}
	if err := r.executor.Recover(context.Background(), assessment); err != nil {
		t.Fatal(err)
	}
	if r.state != Succeeded || !r.committed || r.release.ID != source.ID || r.release.PlanID != source.PlanID || !reflect.DeepEqual(r.checks, []bool{false, true}) || !reflect.DeepEqual(r.owners, []string{source.ID}) {
		t.Fatal(r.state, r.release.ID, r.checks, r.owners)
	}
	if !reflect.DeepEqual(r.effects, []string{"commit"}) {
		t.Fatal("recovery replayed component effects", r.effects)
	}
	if !reflect.DeepEqual(original, r.events[:len(original)]) {
		t.Fatal("original prefix changed")
	}
	for _, call := range r.executor.Managed.(*managedRuntimeFake).calls {
		if call != "managed_completed" {
			t.Fatal("recovery mutated native state", call)
		}
	}
}
func TestManagedRouteResolutionRefusesChangedEvidence(t *testing.T) {
	cases := map[string]func(*managedRouteRecoveryRig){
		"policy":     func(r *managedRouteRecoveryRig) { r.facts.Input.Desired.PolicyHash = "changed" },
		"generation": func(r *managedRouteRecoveryRig) { r.facts.Input.State.Generation++ },
		"target":     func(r *managedRouteRecoveryRig) { r.facts.Input.State.Target.ID = "changed" },
		"unit": func(r *managedRouteRecoveryRig) {
			(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits.Value = &[]target.Unit{{Name: r.plan.App + ".container", Hash: "sha256:" + strings.Repeat("d", 64)}}
		},
		"image": func(r *managedRouteRecoveryRig) {
			(*r.facts.Input.Snapshot.Apps.Value)[0].Image.Value.Digest = "changed"
		},
		"route": func(r *managedRouteRecoveryRig) { r.loadedFailure = true },
		"runtime": func(r *managedRouteRecoveryRig) {
			r.executor.Managed.(*managedRuntimeFake).failure = "managed_completed"
		},
		"release": func(r *managedRouteRecoveryRig) { r.hasRelease = true },
		"earlier-unknown": func(r *managedRouteRecoveryRig) {
			for i, event := range r.events {
				var step ops.StepPayload
				if event.Kind == "step" && json.Unmarshal(event.Payload, &step) == nil && step.Step == "frontend_start" && step.Outcome == "completed" {
					step.Outcome = "unknown"
					r.events[i].Payload, _ = json.Marshal(step)
					return
				}
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r, source, resolver := managedRouteRecoveryFixture(t)
			mutate(r)
			assessment, err := r.executor.InspectResolution(context.Background(), resolver, source, r.plan, r.desired, r.events)
			if err != nil || assessment.Action != RequireRecovery || len(r.effects) != 0 || r.committed {
				t.Fatal(assessment.Action, err, r.effects)
			}
		})
	}
}
func TestManagedRouteResolutionRechecksAfterAssessment(t *testing.T) {
	r, source, resolver := managedRouteRecoveryFixture(t)
	assessment, err := r.executor.InspectResolution(context.Background(), resolver, source, r.plan, r.desired, r.events)
	if err != nil || assessment.Action != ResumeForward {
		t.Fatal(err, assessment.Action)
	}
	r.facts.Input.State.Generation++
	if err := r.executor.Recover(context.Background(), assessment); err == nil || r.committed || len(r.checks) != 0 || len(r.effects) != 0 {
		t.Fatal("stale assessment committed", err)
	}
}

func TestManagedRouteResolutionHealthFailureAndCommitRaceDoNotAdoptArtifacts(t *testing.T) {
	for _, race := range []bool{false, true} {
		r, source, resolver := managedRouteRecoveryFixture(t)
		assessment, err := r.executor.InspectResolution(context.Background(), resolver, source, r.plan, r.desired, r.events)
		if err != nil || assessment.Action != ResumeForward {
			t.Fatal(err, assessment.Action)
		}
		if race {
			r.afterHealth = func() { r.facts.Input.State.Generation++ }
		} else {
			r.checkFailure = true
		}
		if err := r.executor.Recover(context.Background(), assessment); err == nil || r.committed || len(r.effects) != 0 || r.state != RecoveryRequired {
			t.Fatal("health or commit-time refusal adopted artifacts", err, r.effects, r.state)
		}
	}
}
func TestManagedCreateLatePrefixRejectsMissingDuplicateAndEarlierUnknownSteps(t *testing.T) {
	r, _, _ := managedRouteRecoveryFixture(t)
	steps := []Event{}
	for _, event := range r.events {
		if event.Kind == "step" {
			steps = append(steps, event)
		}
	}
	if _, valid := ops.ManagedCreateRecoveryPrefix(steps, r.plan); !valid {
		t.Fatal("known prefix rejected")
	}
	for i := range steps {
		missing := append([]Event(nil), steps[:i]...)
		missing = append(missing, steps[i+1:]...)
		if _, valid := ops.ManagedCreateRecoveryPrefix(missing, r.plan); valid && i < len(steps)-1 {
			t.Fatal("missing prefix event accepted", i)
		}
		duplicate := append([]Event(nil), steps[:i+1]...)
		duplicate = append(duplicate, steps[i:]...)
		if _, valid := ops.ManagedCreateRecoveryPrefix(duplicate, r.plan); valid {
			t.Fatal("duplicated prefix event accepted", i)
		}
	}
}

func TestOriginalManagedCommitUnknownDoesNotAdvertiseUncommittedAdoption(t *testing.T) {
	for _, state := range []State{Queued, LaunchUnknown, Preflight, Preparing, Quiescing, Starting, Checking, Committing} {
		t.Run(string(state), func(t *testing.T) {
			r, source, resolver := managedRouteRecoveryFixture(t)
			addManagedRecoverySteps(r, ops.StepPayload{Step: "managed_route_check", Outcome: "completed"}, ops.StepPayload{Step: "managed_commit", Outcome: "intent"}, ops.StepPayload{Step: "managed_commit", Outcome: "unknown"})
			resolver.State = state
			assessment, err := r.executor.InspectResolution(context.Background(), resolver, source, r.plan, r.desired, r.events)
			if err != nil || assessment.Action != RequireRecovery || len(r.effects) != 0 || len(r.checks) != 0 || r.committed {
				t.Fatal("unsupported original commit advertised", state, assessment.Action, err, r.effects)
			}
			original := source
			original.State = Committing
			assessment, err = r.executor.InspectRecovery(context.Background(), original, r.plan, r.desired, r.events)
			if err != nil || assessment.Action != RequireRecovery {
				t.Fatal("unsupported original commit reconciliation advertised", assessment.Action, err)
			}
		})
	}
}
func addManagedRecoverySteps(r *managedRouteRecoveryRig, steps ...ops.StepPayload) {
	for _, step := range steps {
		raw, _ := json.Marshal(step)
		r.events = append(r.events, Event{Kind: "step", Payload: raw})
	}
}

type stoppedManagedRouteFake struct{ *managedRuntimeFake }

func (f stoppedManagedRouteFake) CompletedManaged(context.Context, string, plan.Plan, policy.Desired) error {
	return injected
}
func (f stoppedManagedRouteFake) StoppedManagedFrontend(context.Context, string, plan.Plan, policy.Desired) error {
	return nil
}

func TestStoppedManagedRecoveryAssessmentMatchesWakeBoundary(t *testing.T) {
	states := []State{Queued, LaunchUnknown, Preflight, Preparing, Quiescing, Starting, Checking, Committing}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			for _, outcome := range []string{"intent", "unknown", "completed", "commit-unknown"} {
				t.Run(outcome, func(t *testing.T) {
					r, source, resolver := managedRouteRecoveryFixture(t)
					r.executor.Managed = stoppedManagedRouteFake{r.executor.Managed.(*managedRuntimeFake)}
					resolver.State = state
					if outcome == "intent" {
						r.events = slices.DeleteFunc(r.events, func(event Event) bool {
							var step ops.StepPayload
							return event.Kind == "step" && json.Unmarshal(event.Payload, &step) == nil && step.Step == "managed_route_check" && step.Outcome == "unknown"
						})
					}
					if outcome == "completed" || outcome == "commit-unknown" {
						addManagedRecoverySteps(r, ops.StepPayload{Step: "managed_route_check", Outcome: "completed"})
					}
					if outcome == "commit-unknown" {
						addManagedRecoverySteps(r, ops.StepPayload{Step: "managed_commit", Outcome: "intent"}, ops.StepPayload{Step: "managed_commit", Outcome: "unknown"})
						// A nested resolver commit is a running-only continuation; it must not
						// advertise another wake after the route check has completed.
						source.Kind = ops.Resolve
						source.RecoveryOf = "original"
						r.executor.Journal = managedRootJournal{rig: r.rig, root: Operation{ID: "original", Kind: ops.Deploy, PlanID: r.plan.Hash, App: r.plan.App, State: RecoveryRequired}}
					}
					assessment, err := r.executor.InspectResolution(context.Background(), resolver, source, r.plan, r.desired, r.events)
					wanted := RequireRecovery
					if outcome == "intent" || outcome == "unknown" {
						wanted = ResumeForward
					}
					if err != nil || assessment.Action != wanted || len(r.effects) != 0 || len(r.checks) != 0 || r.committed {
						t.Fatal("assessment and wake grant disagree", outcome, state, assessment.Action, err, r.effects)
					}
				})
			}
		})
	}
}

type managedRootJournal struct {
	*rig
	root Operation
}

func (j managedRootJournal) GetOperation(_ context.Context, id string) (Operation, error) {
	if id == j.root.ID {
		return j.root, nil
	}
	return Operation{}, injected
}

func TestCompletedRouteCheckCannotGainWakeAfterAssessment(t *testing.T) {
	r, source, resolver := managedRouteRecoveryFixture(t)
	addManagedRecoverySteps(r, ops.StepPayload{Step: "managed_route_check", Outcome: "completed"})
	assessment, err := r.executor.InspectResolution(context.Background(), resolver, source, r.plan, r.desired, r.events)
	if err != nil || assessment.Action != ResumeForward {
		t.Fatal("running completion unavailable", err, assessment.Action)
	}
	r.executor.Managed = stoppedManagedRouteFake{r.executor.Managed.(*managedRuntimeFake)}
	if err := r.executor.Recover(context.Background(), assessment); err == nil || r.committed || len(r.effects) != 0 || len(r.checks) != 0 {
		t.Fatal("liveness change authorized unadmitted wake", err, r.effects)
	}
}
