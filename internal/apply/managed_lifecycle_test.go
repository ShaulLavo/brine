package apply

import (
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/caddy"
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
	runtime.failure = "route_permit"
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
