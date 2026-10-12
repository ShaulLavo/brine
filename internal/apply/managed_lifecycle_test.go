package apply

import (
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/plan"
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
		if call == "engine_install" || call == "engine_stage" || call == "frontend_bind" || frontend && (call == "engine_stopped" || call == "engine_start_permit") {
			t.Fatal("component effects crossed", frontend, action, runtime.calls)
		}
	}
	for _, effect := range r.effects {
		if effect == "install_unit" || effect == "remove_unit" || effect == "commit" || effect == "pull_image" {
			t.Fatal("lifecycle changed artifacts", effect)
		}
	}
}
