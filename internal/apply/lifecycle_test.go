package apply

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

func lifecycleRig(t *testing.T, action plan.ChangeKind) *rig {
	t.Helper()
	r := newRig(t, true)
	r.desired = r.oldDesired
	r.facts.Input.Desired = r.oldDesired
	p, err := plan.BuildLifecycle(r.facts.Input, action)
	if err != nil || p.Kind == plan.Conflict {
		t.Fatal(p, err)
	}
	r.plan = p
	r.executor.Systemd.(*systemd.Fake).JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return false, nil }
	return r
}
func TestLifecycleApplyOnlyTouchesOwnedUnit(t *testing.T) {
	for _, action := range []plan.ChangeKind{plan.StopApp, plan.StartApp, plan.RestartApp} {
		t.Run(string(action), func(t *testing.T) {
			r := lifecycleRig(t, action)
			if action == plan.StartApp {
				r.active = false
			}
			r.executor.Systemd.(*systemd.Fake).ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
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
			if err := r.executor.Run(context.Background(), "operation-1", r.plan, r.desired); err != nil {
				t.Fatal(err)
			}
			if r.state != Succeeded || r.active == (action == plan.StopApp) || r.committed {
				t.Fatal(r.state, r.active, r.committed)
			}
			for _, effect := range r.effects {
				if effect != "preflight" && effect != "stop_unit" && effect != "reload_units" && effect != "start_unit" && effect != "check_direct" {
					t.Fatal("unrelated effect", effect)
				}
			}
			if action == plan.StopApp && strings.Contains(strings.Join(r.effects, " "), "start_unit") {
				t.Fatal("stop started app")
			}
		})
	}
}
func TestLifecycleStopIdempotent(t *testing.T) {
	r := lifecycleRig(t, plan.StopApp)
	r.active = false
	r.executor.Systemd.(*systemd.Fake).ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
		return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
	}
	if err := r.executor.Run(context.Background(), "operation-1", r.plan, r.desired); err != nil {
		t.Fatal(err)
	}
	for _, e := range r.effects {
		if e == "stop_unit" {
			t.Fatal("already-stopped unit mutated")
		}
	}
}
func TestLifecycleUnknownStopSettlesManagerJobs(t *testing.T) {
	for _, settled := range []bool{false, true} {
		t.Run(strings.ToLower(strings.TrimSpace(map[bool]string{false: "pending", true: "settled"}[settled])), func(t *testing.T) {
			r := lifecycleRig(t, plan.StopApp)
			r.executor.EffectTimeout = 15 * time.Millisecond
			r.executor.Systemd.(*systemd.Fake).StopFunc = func(context.Context, systemd.Unit) error {
				r.active = false
				return &localexec.Error{Kind: localexec.UnknownOutcome}
			}
			r.executor.Systemd.(*systemd.Fake).ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
				return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
			}
			r.executor.Systemd.(*systemd.Fake).JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return !settled, nil }
			err := r.executor.Run(context.Background(), "operation-1", r.plan, r.desired)
			if settled && (err != nil || r.state != Succeeded) || !settled && (err == nil || r.state != RecoveryRequired) {
				t.Fatal(err, r.state)
			}
		})
	}
}
func TestExecutorRefusesSecretOperation(t *testing.T) {
	r := newRig(t, false)
	r.operationKind = ops.SecretSet
	if err := r.executor.Run(context.Background(), "operation-1", r.plan, r.desired); err == nil || len(r.effects) != 0 {
		t.Fatal("non-deploy executed", err, r.effects)
	}
}

func TestLifecycleStopThenFreshPlanAndApply(t *testing.T) {
	for _, action := range []plan.ChangeKind{plan.StartApp, plan.RestartApp, plan.StopApp} {
		t.Run(string(action), func(t *testing.T) {
			r := lifecycleRig(t, plan.StopApp)
			r.executor.Systemd.(*systemd.Fake).ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
				if r.active {
					return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
				}
				return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
			}
			if err := r.executor.Run(context.Background(), "operation-1", r.plan, r.desired); err != nil {
				t.Fatal(err)
			}
			if r.active {
				t.Fatal("stop did not stop")
			}
			// Fresh inventory: no runtime publication or listener, but independently
			// verified owned-unit pins retain image and port allocation (inventory tests).
			inactive := target.Known(false)
			(*r.facts.Input.Snapshot.Apps.Value)[0].UnitActive = &inactive
			r.facts.Input.Snapshot.UsedPorts = target.Known([]target.Port{})
			r.facts.Input.Snapshot.PortOwners = target.Known([]target.PortOwner{})
			p, err := plan.BuildLifecycle(r.facts.Input, action)
			if err != nil || p.Kind != plan.Update {
				t.Fatal(p, err)
			}
			r.plan = p
			r.state = Queued
			r.events = nil
			r.states = nil
			r.effects = nil
			r.intent = ""
			if err := r.executor.Run(context.Background(), "operation-2", p, r.desired); err != nil {
				t.Fatal("fresh preflight refused", err)
			}
			if r.state != Succeeded || r.active != (action != plan.StopApp) || r.committed {
				t.Fatal(r.state, r.active, r.committed)
			}
			if action == plan.StopApp {
				for _, effect := range r.effects {
					if effect == "stop_unit" {
						t.Fatal("repeated stop mutated unit")
					}
				}
			}
		})
	}
}
