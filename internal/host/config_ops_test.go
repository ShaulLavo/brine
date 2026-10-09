package host

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type lifecycleInventory struct {
	base   *fakeInventory
	active *bool
}

func (i lifecycleInventory) Collect(ctx context.Context) (target.Snapshot, error) {
	snap, err := i.base.Collect(ctx)
	if err != nil {
		return snap, err
	}
	active := target.Known(*i.active)
	for n := range *snap.Apps.Value {
		(*snap.Apps.Value)[n].UnitActive = &active
	}
	if !*i.active {
		snap.UsedPorts = target.Known([]target.Port{})
		snap.PortOwners = target.Known([]target.PortOwner{})
	}
	return snap, nil
}

func TestConnectedLifecycleAfterDeployUsesProductionPreflight(t *testing.T) {
	r := newDeployRig(t)
	initial := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: initial.PlanID, IdempotencyKey: "initial-deploy"}).Data.(jobs.Accepted)
	if err := r.runner.Run(context.Background(), accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	active := true
	inventory := lifecycleInventory{r.inventory, &active}
	r.service.Inventory = inventory
	appService := apps.Service{Store: r.store, Inventory: inventory, LoadPolicy: r.policy.Load}
	r.server.Config = appService
	executor := r.runner.Executor.(Executor)
	executor.Service = r.service
	manager := executor.Engine.Systemd.(*systemd.Fake)
	manager.StopFunc = func(context.Context, systemd.Unit) error { active = false; return nil }
	manager.StartFunc = func(context.Context, systemd.Unit) error { active = true; return nil }
	manager.IsActiveFunc = func(context.Context, systemd.Unit) (bool, error) { return active, nil }
	manager.ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
		if active {
			return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
		}
		return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
	}
	manager.ShowFunc = func(_ context.Context, unit systemd.Unit) (systemd.Properties, error) {
		if unit.String() == "hello.service" && active {
			return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
		}
		return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
	}
	executor.Engine.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
		if active {
			return podman.ContainerState{Running: true, Status: "running"}, nil
		}
		return podman.ContainerState{Status: "exited"}, nil
	}
	r.runner.Executor = executor
	for n, action := range []plan.ChangeKind{plan.StopApp, plan.StopApp, plan.StartApp, plan.StopApp, plan.RestartApp} {
		planned := r.call(t, "lifecycle", dispatch.LifecycleArgs{App: "hello", Action: action}).Data.(apps.ConfigPlan)
		if planned.Kind != plan.Update || planned.Lifecycle != action {
			t.Fatal(planned)
		}
		job := r.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: fmt.Sprintf("lifecycle-%d", n)}).Data.(jobs.Accepted)
		if err := r.runner.Run(context.Background(), job.OperationID); err != nil {
			t.Fatal("production lifecycle preflight refused", action, err)
		}
		status := r.call(t, "operation", dispatch.OperationArgs{OperationID: job.OperationID}).Data.(jobs.Status)
		if status.Operation.Kind != ops.Deploy || status.Operation.State != ops.Succeeded || active != (action != plan.StopApp) {
			t.Fatal(status, active)
		}
	}
	if r.pulls != 1 || r.units.installs != 1 {
		t.Fatal("lifecycle installed deployment artifacts", r.pulls, r.units.installs)
	}
	release, err := r.store.CurrentRelease(context.Background(), "hello")
	if err != nil || release.PlanID != initial.PlanID {
		t.Fatal("lifecycle committed a new release", release, err)
	}
}

func TestConnectedConfigIsPlanOnlyUntilApply(t *testing.T) {
	r := newDeployRig(t)
	initial := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	job := r.call(t, "apply", dispatch.ApplyArgs{PlanID: initial.PlanID, IdempotencyKey: "config-initial"}).Data.(jobs.Accepted)
	if err := r.runner.Run(context.Background(), job.OperationID); err != nil {
		t.Fatal(err)
	}
	r.server.Config = apps.Service{Store: r.store, Inventory: r.inventory, LoadPolicy: r.policy.Load}
	changed := r.call(t, "config_set", dispatch.ConfigArgs{App: "hello", Edits: []apps.Edit{{Key: "environment.CONFIG_TEST", Value: "private-test-value", Action: "set"}}}).Data.(apps.ConfigPlan)
	if changed.Kind != plan.Update {
		t.Fatal(changed)
	}
	release, err := r.store.CurrentRelease(context.Background(), "hello")
	if err != nil || release.PlanID != initial.PlanID || r.pulls != 1 || r.units.installs != 1 {
		t.Fatal("config mutated before apply", release, err)
	}
	p, d, err := r.store.LoadPlan(context.Background(), changed.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.CanonicalBytes()
	if err != nil || strings.Contains(string(raw), "private-test-value") {
		t.Fatal("environment leaked into executable plan", err)
	}
	found := false
	for _, env := range d.Environment {
		if env.Name == "CONFIG_TEST" && env.Value == "private-test-value" {
			found = true
		}
	}
	if !found {
		t.Fatal("private desired input not saved")
	}
	executor := r.runner.Executor.(Executor)
	manager := executor.Engine.Systemd.(*systemd.Fake)
	active := true
	manager.StopFunc = func(context.Context, systemd.Unit) error { active = false; return nil }
	manager.StartFunc = func(context.Context, systemd.Unit) error { active = true; return nil }
	manager.IsActiveFunc = func(context.Context, systemd.Unit) (bool, error) { return active, nil }
	manager.ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
		if active {
			return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
		}
		return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
	}
	executor.Engine.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
		return podman.ContainerState{Running: active, Status: map[bool]string{true: "running", false: "exited"}[active]}, nil
	}
	r.runner.Executor = executor
	applied := r.call(t, "apply", dispatch.ApplyArgs{PlanID: changed.PlanID, IdempotencyKey: "config-apply"}).Data.(jobs.Accepted)
	if err := r.runner.Run(context.Background(), applied.OperationID); err != nil {
		t.Fatal(err)
	}
	release, err = r.store.CurrentRelease(context.Background(), "hello")
	if err != nil || release.PlanID != changed.PlanID {
		t.Fatal("config release not committed", release, err)
	}
}
