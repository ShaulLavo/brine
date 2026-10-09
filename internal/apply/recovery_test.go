package apply

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

func recoveryEvents(names ...string) []Event {
	events := []Event{}
	for i, name := range names {
		outcome := "completed"
		if i == len(names)-1 {
			outcome = "intent"
		}
		payload, _ := json.Marshal(ops.StepPayload{Step: name, Outcome: outcome})
		events = append(events, Event{Sequence: uint64(i + 1), Kind: "step", Payload: payload})
	}
	return events
}

func TestRecoveryNeverReplaysUnknownStage(t *testing.T) {
	r := newRig(t, false)
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	events := recoveryEvents("preflight", "pull_image", "verify_image", "ensure_secrets", "stage_unit")
	assessment, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation", PlanID: r.plan.Hash, State: Preparing}, r.plan, r.desired, events)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Action != RequireRecovery {
		t.Fatalf("action %s", assessment.Action)
	}
	if len(r.events) != 0 || len(r.effects) != 0 {
		t.Fatal("inspection mutated host")
	}
}

func TestRecoveryContinuesAfterProvenPullWithoutPullingAgain(t *testing.T) {
	r := newRig(t, false)
	configureSettledRecoveryWriter(r)
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	r.intent = "pull_image"
	r.state = Preparing
	events := recoveryEvents("preflight", "pull_image")
	assessment, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation", PlanID: r.plan.Hash, State: Preparing}, r.plan, r.desired, events)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Action != ResumeForward {
		t.Fatalf("action %s", assessment.Action)
	}
	if err := r.executor.Recover(context.Background(), assessment); err != nil {
		t.Fatal(err)
	}
	if r.state != Succeeded {
		t.Fatalf("state %s", r.state)
	}
	for _, effect := range r.effects {
		if effect == "pull_image" {
			t.Fatal("replayed pull")
		}
	}
}

func TestRecoveryIntentCrashMatrix(t *testing.T) {
	for i, step := range forwardSteps {
		t.Run(step, func(t *testing.T) {
			r := newRig(t, false)
			r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
			r.intent = step
			fake := r.executor.Systemd.(*systemd.Fake)
			fake.ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
				return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
			}
			fake.JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return false, nil }
			state := Preparing
			switch step {
			case "preflight":
				state = Preflight
			case "quiesce_old":
				state = Quiescing
			case "install_unit", "reload_units", "start_unit":
				state = Starting
			case "check_direct", "publish_route", "check_routed":
				state = Checking
			case "commit":
				state = Committing
			}
			op := Operation{ID: "operation", PlanID: r.plan.Hash, State: state}
			assessment, err := r.executor.InspectRecovery(context.Background(), op, r.plan, r.desired, recoveryEvents(forwardSteps[:i+1]...))
			if err != nil {
				t.Fatal(err)
			}
			want := RequireRecovery
			switch step {
			case "preflight", "pull_image", "verify_image", "ensure_secrets", "quiesce_old":
				want = ResumeForward
			}
			if assessment.Action != want {
				t.Fatalf("action %s want %s", assessment.Action, want)
			}
			if len(r.events) != 0 || len(r.effects) != 0 {
				t.Fatalf("inspection had effects %v events %v", r.effects, r.events)
			}
		})
	}
	for _, step := range []string{"rollback_quiesce", "rollback_unit", "rollback_reload", "rollback_route", "check_compatibility", "rollback_start", "rollback_check"} {
		t.Run(step, func(t *testing.T) {
			r := newRig(t, true)
			r.intent = step
			r.executor.Systemd.(*systemd.Fake).ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) { return systemd.Properties{}, injected }
			r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
			assessment, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation", PlanID: r.plan.Hash, State: RollingBack}, r.plan, r.desired, recoveryEvents(step))
			if err != nil || assessment.Action != RequireRecovery || len(r.effects) != 0 {
				t.Fatalf("assessment %+v err %v effects %v", assessment, err, r.effects)
			}
		})
	}
}

func TestRecoveryProvenInstallRollsBackStalePlan(t *testing.T) {
	r := newRig(t, true)
	r.state = Starting
	r.active = false
	r.intent = "install_unit"
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	unit, err := quadlet.Render(r.desired, r.plan, *r.plan.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits = target.Known([]target.Unit{{Name: unit.Name(), Hash: unit.Hash()}})
	fake := r.executor.Systemd.(*systemd.Fake)
	fake.ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
		return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
	}
	fake.JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return false, nil }
	events := recoveryEvents(forwardSteps[:7]...)
	assessment, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation", PlanID: r.plan.Hash, State: Starting}, r.plan, r.desired, events)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Action != RestorePrevious {
		t.Fatalf("action %s", assessment.Action)
	}
	err = r.executor.Recover(context.Background(), assessment)
	var failure *Error
	if !errors.As(err, &failure) || r.state != RolledBack {
		t.Fatalf("error %v state %s", err, r.state)
	}
	for _, effect := range r.effects {
		if effect == "install_unit" || effect == "pull_image" || effect == "publish_route" {
			t.Fatalf("replayed forward effect %s", effect)
		}
	}
}

func TestRecoveryRefusesMismatchedEvidence(t *testing.T) {
	for _, events := range [][]Event{recoveryEvents("install_unit"), recoveryEvents("preflight", "publish_route"), recoveryEvents("preflight", "pull_image", "pull_image")} {
		r := newRig(t, false)
		assessment, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation", PlanID: r.plan.Hash, State: Starting}, r.plan, r.desired, events)
		if err != nil || assessment.Action != RequireRecovery {
			t.Fatalf("assessment %+v err %v", assessment, err)
		}
	}
}

func TestRecoveryRefusesNewerCommittedGeneration(t *testing.T) {
	r := newRig(t, true)
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	r.facts.Input.State.Generation++
	assessment, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation", PlanID: r.plan.Hash, State: Starting}, r.plan, r.desired, recoveryEvents(forwardSteps[:7]...))
	if err != nil || assessment.Action != RequireRecovery || len(r.effects) != 0 {
		t.Fatalf("assessment %+v error %v effects %v", assessment, err, r.effects)
	}
}

func TestRecoveryInventoryTimeoutIsBounded(t *testing.T) {
	r := newRig(t, false)
	r.executor.EffectTimeout = 10 * time.Millisecond
	r.executor.Facts = FactsFunc(func(ctx context.Context) (Facts, error) { <-ctx.Done(); return Facts{}, ctx.Err() })
	_, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation", PlanID: r.plan.Hash, State: Preparing}, r.plan, r.desired, recoveryEvents("preflight", "pull_image"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v", err)
	}
}

func TestRecoveryCommitReadBackFinishesWithoutEffects(t *testing.T) {
	r := newRig(t, false)
	configureSettledRecoveryWriter(r)
	if err := r.run(); err != nil {
		t.Fatal(err)
	}
	r.state = Committing
	r.effects = nil
	r.events = nil
	observeCommittedRelease(r)
	r.facts.Routing.Generation = r.release.CaddyGeneration
	r.facts.Routing.Files = map[string]string{r.release.CaddyFile.Name: r.release.CaddyFile.Hash}
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	assessment, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation-1", PlanID: r.plan.Hash, State: Committing}, r.plan, r.desired, recoveryEvents(forwardSteps...))
	if err != nil || assessment.Action != FinishSucceeded {
		t.Fatalf("assessment %+v error %v", assessment, err)
	}
	if err := r.executor.Recover(context.Background(), assessment); err != nil {
		t.Fatal(err)
	}
	if r.state != Succeeded || len(r.effects) != 0 {
		t.Fatalf("state %s effects %v", r.state, r.effects)
	}
}

func TestRecoveryProofJournalFailurePreventsContinuation(t *testing.T) {
	r := newRig(t, false)
	configureSettledRecoveryWriter(r)
	r.state = Preparing
	r.intent = "pull_image"
	r.failOutcome = "pull_image"
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	assessment, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation", PlanID: r.plan.Hash, State: Preparing}, r.plan, r.desired, recoveryEvents("preflight", "pull_image"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.executor.Recover(context.Background(), assessment); err == nil {
		t.Fatal("ignored journal failure")
	}
	if len(r.effects) != 0 || r.state != Preparing {
		t.Fatalf("effects %v state %s", r.effects, r.state)
	}
}

func TestRecoveryAssessmentCannotBeReusedOrChanged(t *testing.T) {
	r := newRig(t, false)
	configureSettledRecoveryWriter(r)
	r.state = Preparing
	r.intent = "pull_image"
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	assessment, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation", PlanID: r.plan.Hash, State: Preparing}, r.plan, r.desired, recoveryEvents("preflight", "pull_image"))
	if err != nil {
		t.Fatal(err)
	}
	changed := assessment
	changed.Action = RestorePrevious
	if err := r.executor.Recover(context.Background(), changed); err == nil {
		t.Fatal("accepted changed recovery action")
	}
	if err := r.executor.Recover(context.Background(), assessment); err != nil {
		t.Fatal(err)
	}
	count := len(r.effects)
	if err := r.executor.Recover(context.Background(), assessment); err == nil || len(r.effects) != count {
		t.Fatalf("repeat error %v effects %v", err, r.effects)
	}
}

func TestRecoveryPreflightKeepsEffectDeadline(t *testing.T) {
	r := newRig(t, false)
	configureSettledRecoveryWriter(r)
	r.state = Preparing
	r.intent = "pull_image"
	reads := 0
	r.executor.Facts = FactsFunc(func(ctx context.Context) (Facts, error) {
		reads++
		if reads == 1 {
			return r.facts, nil
		}
		<-ctx.Done()
		return Facts{}, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	assessment, err := r.executor.InspectRecovery(ctx, Operation{ID: "operation", PlanID: r.plan.Hash, State: Preparing}, r.plan, r.desired, recoveryEvents("preflight", "pull_image"))
	if err != nil || assessment.Action != ResumeForward {
		t.Fatalf("action %s error %v", assessment.Action, err)
	}
	r.executor.EffectTimeout = 10 * time.Millisecond
	start := time.Now()
	err = r.executor.Recover(ctx, assessment)
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("resumed preflight lost the per-effect deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) || r.state != RecoveryRequired || len(r.effects) != 0 {
		t.Fatalf("error %v state %s effects %v", err, r.state, r.effects)
	}
}

func TestRecoveryCommitRequiresLiveInstalledUnit(t *testing.T) {
	for _, evidence := range []string{"absent", "unknown", "mismatched"} {
		t.Run(evidence, func(t *testing.T) {
			r := newRig(t, false)
			if err := r.run(); err != nil {
				t.Fatal(err)
			}
			r.state = Committing
			r.effects = nil
			observeCommittedRelease(r)
			if evidence == "absent" {
				r.facts.Input.Snapshot.Apps = target.Known([]target.App{})
			}
			r.facts.Routing.Generation = r.release.CaddyGeneration
			r.facts.Routing.Files = map[string]string{r.release.CaddyFile.Name: r.release.CaddyFile.Hash}
			if evidence == "unknown" {
				r.facts.Input.Snapshot.Apps = target.Observation[[]target.App]{Status: target.Unknown}
			}
			if evidence == "mismatched" {
				r.facts.Input.Snapshot.Apps = target.Known([]target.App{{Name: r.plan.App, QuadletUnits: target.Known([]target.Unit{{Name: r.plan.App + ".container", Hash: "sha256:" + strings.Repeat("f", 64)}})}})
			}
			r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
			assessment, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation-1", PlanID: r.plan.Hash, State: Committing}, r.plan, r.desired, recoveryEvents(forwardSteps...))
			if err != nil || assessment.Action != RequireRecovery {
				t.Fatalf("action %s error %v", assessment.Action, err)
			}
			if len(r.effects) != 0 {
				t.Fatal(r.effects)
			}
		})
	}
}

func observeCommittedRelease(r *rig) {
	secrets := []target.Secret{}
	for _, binding := range r.release.Secrets {
		secrets = append(secrets, target.Secret{Name: binding.VersionName, ID: binding.ID})
	}
	r.facts.Input.Snapshot.Apps = target.Known([]target.App{{
		Name:              r.plan.App,
		Image:             target.Known(target.Image{Digest: r.release.Image.Digest, Platform: r.release.Image.Platform}),
		AllocatedHostPort: target.Known(r.release.HostPort),
		QuadletUnits:      target.Known(append([]target.Unit{}, r.release.Units...)),
		Secrets:           target.Known(secrets),
	}})
	r.facts.Input.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: r.release.CaddyGeneration, Files: []target.CaddyFile{r.release.CaddyFile}})
	r.facts.Routing.Generation = r.release.CaddyGeneration
	r.facts.Routing.Files = map[string]string{r.release.CaddyFile.Name: r.release.CaddyFile.Hash}
}

func TestRecoveryCommitRequiresOtherLiveArtifacts(t *testing.T) {
	cases := map[string]func(*rig){
		"retained_unit_missing": func(r *rig) {
			app := &(*r.facts.Input.Snapshot.Apps.Value)[0]
			app.QuadletUnits = target.Known((*app.QuadletUnits.Value)[:1])
		},
		"units_unknown": func(r *rig) {
			(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits = target.Observation[[]target.Unit]{Status: target.Unknown}
		},
		"image_mismatch": func(r *rig) {
			(*r.facts.Input.Snapshot.Apps.Value)[0].Image.Value.Digest = "sha256:" + strings.Repeat("f", 64)
		},
		"port_mismatch": func(r *rig) {
			(*r.facts.Input.Snapshot.Apps.Value)[0].AllocatedHostPort = target.Known(target.Port(21000))
		},
		"secret_missing": func(r *rig) { (*r.facts.Input.Snapshot.Apps.Value)[0].Secrets = target.Known([]target.Secret{}) },
		"secret_unknown": func(r *rig) {
			(*r.facts.Input.Snapshot.Apps.Value)[0].Secrets = target.Observation[[]target.Secret]{Status: target.Unknown}
		},
		"caddy_unknown": func(r *rig) {
			r.facts.Input.Snapshot.CaddyConfig = target.Observation[target.CaddyConfigSet]{Status: target.Unknown}
		},
		"caddy_generation_mismatch": func(r *rig) { r.facts.Input.Snapshot.CaddyConfig.Value.Generation++ },
		"caddy_file_mismatch": func(r *rig) {
			r.facts.Input.Snapshot.CaddyConfig.Value.Files[0].Hash = "sha256:" + strings.Repeat("f", 64)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, true)
			if err := r.run(); err != nil {
				t.Fatal(err)
			}
			r.state = Committing
			r.effects = nil
			observeCommittedRelease(r)
			change(r)
			r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
			assessment, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "operation-1", PlanID: r.plan.Hash, State: Committing}, r.plan, r.desired, recoveryEvents(forwardSteps...))
			if err != nil || assessment.Action != RequireRecovery {
				t.Fatalf("action %s error %v", assessment.Action, err)
			}
			r.executor.Recover(context.Background(), assessment)
			if r.state != RecoveryRequired || len(r.effects) != 0 {
				t.Fatalf("state %s effects %v", r.state, r.effects)
			}
		})
	}
}

// Positive recovery fixtures explicitly supply authoritative manager/container facts.
func configureSettledRecoveryWriter(r *rig) {
	manager := r.executor.Systemd.(*systemd.Fake)
	manager.JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return false, nil }
	manager.ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
		if r.active {
			return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
		}
		return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
	}
	r.executor.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
		if r.active {
			return podman.ContainerState{Running: true, Status: "running"}, nil
		}
		return podman.ContainerState{Status: "exited"}, nil
	}
}
