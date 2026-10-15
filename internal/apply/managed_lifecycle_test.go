package apply

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

func managedLifecycleRig(t *testing.T, frontend bool, action plan.ChangeKind) (*rig, *managedRuntimeFake) {
	t.Helper()
	r := managedInitializedRig(t, frontend)
	runtime := &managedRuntimeFake{rig: r}
	r.executor.Managed = runtime
	if err := r.executor.Run(context.Background(), "deploy", r.plan, r.desired); err != nil {
		t.Fatal(err)
	}
	r.oldPlan, r.oldDesired = r.plan, r.desired
	m := r.plan.Managed.Clone()
	current := plan.CurrentRelease{App: r.plan.App, ID: r.release.ID, Desired: r.desired, Image: r.release.Image, HostPort: r.release.HostPort, Secrets: r.release.Secrets, Units: r.release.Units, CaddyFile: r.release.CaddyFile, CaddyGeneration: r.release.CaddyGeneration, Managed: &m}
	r.facts.Input.State.Releases = []plan.CurrentRelease{current}
	r.facts.Routing = caddy.State{Generation: r.release.CaddyGeneration, Files: map[string]string{r.release.CaddyFile.Name: r.release.CaddyFile.Hash}}
	r.facts.Input.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: r.release.CaddyGeneration, Files: []target.CaddyFile{r.release.CaddyFile}})
	if frontend {
		front := r.release.Managed.Frontend
		r.facts.Input.Snapshot.Apps = target.Known([]target.App{{Name: r.plan.App, Image: target.Observation[target.Image]{Status: target.Unknown}, AllocatedHostPort: target.Known(front.Port), QuadletUnits: target.Known([]target.Unit{front.Unit}), Secrets: target.Known([]target.Secret{})}})
	}
	r.facts.Input.Managed.Intent = &plan.ManagedIntentFacts{Revision: 1, EpochID: m.Archive.EpochID, RuntimeSHA256: m.RuntimeSHA256, Current: true, EngineRunning: true, FrontendRunning: frontend, IngressServing: true}
	r.facts.Input.Managed.RouteRevision = 1
	p, err := plan.BuildLifecycle(r.facts.Input, action)
	if err != nil || p.Kind == plan.Conflict {
		t.Fatal("managed lifecycle", p.Conflicts, err)
	}
	r.plan = p
	r.committed, r.events, r.effects = false, nil, nil
	r.state, r.intent = Queued, ""
	runtime.calls = nil
	adapter := r.executor.Systemd.(*systemd.Fake)
	stop, start := adapter.StopFunc, adapter.StartFunc
	adapter.StopFunc = func(ctx context.Context, unit systemd.Unit) error {
		if unit.String() == "brine-engine-"+string(m.Database.BindingID)+".service" {
			runtime.engineStopped = true
			return r.hit(r.intent)
		}
		return stop(ctx, unit)
	}
	adapter.StartFunc = func(ctx context.Context, unit systemd.Unit) error {
		if unit.String() == "brine-engine-"+string(m.Database.BindingID)+".service" {
			runtime.engineStopped = false
			return r.hit(r.intent)
		}
		return start(ctx, unit)
	}
	r.executor.Systemd.(*systemd.Fake).JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return false, nil }
	return r, runtime
}
func TestManagedLifecycleKeepsNativeAndFrontendEffectsSeparate(t *testing.T) {
	for _, frontend := range []bool{false, true} {
		for _, action := range []plan.ChangeKind{plan.StopApp, plan.StartApp, plan.RestartApp} {
			testManagedLifecycle(t, frontend, action)
		}
	}
}
func testManagedLifecycle(t *testing.T, frontend bool, action plan.ChangeKind) {
	t.Helper()
	r, runtime := managedLifecycleRig(t, frontend, action)
	if err := r.executor.Run(context.Background(), "lifecycle", r.plan, r.desired); err != nil {
		t.Fatal(frontend, action, err)
	}
	if r.state != Succeeded || r.committed {
		t.Fatal("lifecycle created a release", r.state)
	}
	for _, call := range runtime.calls {
		if call == "engine_install" || call == "engine_stage" || call == "frontend_bind" || frontend && (call == "engine_stopped" || call == "engine_start_permit" || call == "engine_stop_permit") {
			t.Fatal("component effects crossed", frontend, action, runtime.calls)
		}
	}
	for _, effect := range r.effects {
		if effect == "install_unit" || effect == "remove_unit" || effect == "commit" || effect == "pull_image" {
			t.Fatal("lifecycle changed artifacts", effect)
		}
	}
}

func (r *rig) RemoveRetained(ctx context.Context, name, hash string) error {
	return r.Remove(ctx, name, hash)
}
func (r *rig) RestoreRemoved(_ context.Context, name, hash string) error {
	if err := r.hit("managed_restore_removed_frontend"); err != nil {
		return err
	}
	if r.release.Managed == nil || r.release.Managed.Frontend == nil || name != r.release.Managed.Frontend.Unit.Name || hash != r.release.Managed.Frontend.Unit.Hash {
		return injected
	}
	r.setLiveUnits([]target.Unit{r.release.Managed.Frontend.Unit})
	return nil
}
func managedFrontendRemovalRig(t *testing.T) (*rig, *managedRuntimeFake) {
	t.Helper()
	r, runtime := managedLifecycleRig(t, true, plan.StartApp)
	desired := r.oldDesired
	desired.Image, desired.ContainerPort = "", 0
	desired.Resources = policy.Resources{}
	desired.Environment, desired.Secrets = []policy.Environment{}, []policy.Secret{}
	r.desired, r.facts.Input.Desired = desired, desired
	p, err := plan.Build(r.facts.Input)
	if err != nil || p.Kind == plan.Conflict {
		t.Fatal("frontend removal plan", p.Conflicts, err)
	}
	r.plan = p
	return r, runtime
}
func TestManagedFrontendRemovalPreservesNativeAndRetainsClientForFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		testManagedFrontendRemoval(t, fail)
	}
}
func testManagedFrontendRemoval(t *testing.T, fail bool) {
	t.Helper()
	r, runtime := managedFrontendRemovalRig(t)
	if fail {
		runtime.failure = "route_permit"
	}
	err := r.executor.Run(context.Background(), "remove-frontend", r.plan, r.desired)
	if !fail && (err != nil || r.state != Succeeded || r.release.Managed.Frontend != nil || r.active) {
		t.Fatal("frontend not removed", err, r.state)
	}
	if fail && (err == nil || r.state != RolledBack || !r.active || r.committed) {
		t.Fatal("frontend not restored", err, r.state)
	}
	for _, call := range runtime.calls {
		if call == "engine_install" || call == "engine_stop_permit" || call == "engine_start_permit" {
			t.Fatal("frontend removal changed native runtime", call)
		}
	}
}

func TestManagedFailedDeploymentPreservesDeliberatelyPausedFrontendAndRoute(t *testing.T) {
	r, runtime := managedLifecycleRig(t, true, plan.StartApp)
	r.active = false
	r.facts.Input.Managed.Intent.FrontendRunning, r.facts.Input.Managed.Intent.IngressServing = false, false
	r.facts.Routing = caddy.State{Generation: r.facts.Routing.Generation, Files: map[string]string{}}
	r.facts.Input.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: r.facts.Routing.Generation, Files: []target.CaddyFile{}})
	p, err := plan.Build(r.facts.Input)
	if err != nil || p.Kind == plan.Conflict {
		t.Fatal("paused deployment", p.Conflicts, err)
	}
	r.plan = p
	runtime.failure = ""
	r.failStep = "managed_route_check"
	err = r.executor.Run(context.Background(), "resume-attempt", p, r.desired)
	if err == nil || r.state != RolledBack || r.active || r.committed {
		t.Fatal("paused state not preserved", err, r.state, r.active)
	}
	for _, effect := range r.effects {
		if effect == "rollback_start" || effect == "rollback_check" {
			t.Fatal("paused frontend restarted", effect)
		}
	}
}

func TestManagedLifecycleCompletedRecoveryInspectsWithoutRepeatingEffects(t *testing.T) {
	for _, frontend := range []bool{false, true} {
		for _, action := range []plan.ChangeKind{plan.StopApp, plan.StartApp, plan.RestartApp} {
			testManagedLifecycleRecovery(t, frontend, action)
		}
	}
}
func testManagedLifecycleRecovery(t *testing.T, frontend bool, action plan.ChangeKind) {
	t.Helper()
	r, runtime := managedLifecycleRig(t, frontend, action)
	if err := r.executor.Run(context.Background(), "lifecycle", r.plan, r.desired); err != nil {
		t.Fatal(err)
	}
	r.state, r.effects = Checking, nil
	files := map[string]string{}
	if action != plan.StopApp {
		files[r.plan.App+".caddy"] = r.release.CaddyFile.Hash
	}
	r.facts.Routing = caddy.State{Generation: r.release.CaddyGeneration + 1, Files: files}
	if frontend && action != plan.StopApp {
		front := r.plan.Managed.Frontend
		r.facts.Input.Snapshot.Apps = target.Known([]target.App{{Name: r.plan.App, Image: target.Known(target.Image{Digest: front.Image.Digest, Platform: front.Image.Platform}), AllocatedHostPort: target.Known(front.Port)}})
	}
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	recovery, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "lifecycle", PlanID: r.plan.Hash, Kind: "deploy", App: r.plan.App, State: Checking}, r.plan, r.desired, r.events)
	if err != nil || recovery.Action != FinishSucceeded {
		t.Fatal(frontend, action, err, recovery.Action, recovery.Step)
	}
	if err := r.executor.Recover(context.Background(), recovery); err != nil || r.state != Succeeded || len(r.effects) != 0 {
		t.Fatal("lifecycle replayed effects", err, r.effects)
	}
	runtime.failure = "managed_completed"
	refused, err := r.executor.InspectRecovery(context.Background(), Operation{ID: "lifecycle", PlanID: r.plan.Hash, Kind: "deploy", App: r.plan.App, State: Checking}, r.plan, r.desired, r.events)
	if err != nil || refused.Action != RequireRecovery {
		t.Fatal("stale lifecycle observation granted completion", err, refused.Action)
	}
}

type guardedResumeRuntime struct{ *managedRuntimeFake }

func (r guardedResumeRuntime) BeforeFrontendStart(ctx context.Context, op string, p plan.Plan, d policy.Desired, hash string) error {
	if err := r.EngineReady(ctx, p); err != nil {
		return err
	}
	return r.managedRuntimeFake.BeforeFrontendStart(ctx, op, p, d, hash)
}

func TestManagedStartResumesStoppedEngineBeforeOwnedFrontend(t *testing.T) {
	r, runtime := managedLifecycleRig(t, true, plan.StartApp)
	runtime.engineStopped = true
	r.active = false
	r.facts.Input.Managed.NativeState = target.Known("stopped")
	r.facts.Input.Managed.OwnedNativeState = target.Known("stopped")
	r.facts.Input.Managed.Intent.Revision = 7
	r.facts.Input.Managed.Intent.EngineRunning = false
	r.facts.Input.Managed.Intent.FrontendRunning = false
	r.facts.Input.Managed.Intent.IngressServing = false
	p, err := plan.BuildLifecycle(r.facts.Input, plan.StartApp)
	if err != nil || p.Kind == plan.Conflict {
		t.Fatal(err, p.Conflicts)
	}
	r.plan = p
	r.executor.Managed = guardedResumeRuntime{runtime}
	if err := r.executor.Run(t.Context(), "resume-all", p, r.desired); err != nil {
		t.Fatal("all-stopped resume failed", err)
	}
	if runtime.engineStopped || !r.active || r.state != Succeeded || r.committed {
		t.Fatal("resume failed or created a release", runtime.engineStopped, r.active, r.state, r.committed)
	}
	var steps []string
	for _, event := range r.events {
		if event.Kind == "step" {
			var payload ops.StepPayload
			_ = json.Unmarshal(event.Payload, &payload)
			if payload.Outcome == "completed" {
				steps = append(steps, payload.Step)
			}
		}
	}
	if !slices.Equal(steps, []string{"preflight", "managed_resume", "managed_engine_start", "managed_engine_check", "managed_frontend_start", "managed_frontend_check", "managed_route", "managed_route_receipt"}) {
		t.Fatal("engine/front readiness order changed", steps)
	}
	if _, valid := managedLifecycleRecoveryPrefix(r.events, p); !valid {
		t.Fatal("resume steps cannot be recovered without repeating effects")
	}
	for _, effect := range r.effects {
		if effect == "install_unit" || effect == "remove_unit" || effect == "commit" || effect == "pull_image" {
			t.Fatal("resume changed installed artifacts", effect)
		}
	}
}
