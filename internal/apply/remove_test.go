package apply

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/ops"
	"maps"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

func newRemoveRig(t testing.TB) *rig {
	t.Helper()
	r := newRig(t, true)
	r.release.Units = r.release.Units[:1]
	r.facts.Input.State.Releases[0].Units = r.release.Units
	(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits = target.Known(r.release.Units)
	r.desired = r.oldDesired
	r.facts.Input.Desired = r.desired
	p, err := plan.BuildRemove(r.facts.Input)
	if err != nil || p.Kind != plan.Update {
		t.Fatal(p, err)
	}
	r.plan = p
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	r.executor.Systemd.(*systemd.Fake).JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return false, nil }
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
	return r
}
func (r *rig) VerifyRemove(context.Context, string, string) error {
	if r.failStep == "verify_remove" {
		return plan.ErrPersistentData
	}
	return nil
}
func (r *rig) Remove(context.Context, string, string) error {
	if r.active {
		panic("remove while writer runs")
	}
	err := r.hit("remove_unit")
	if err == nil {
		(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits = target.Known([]target.Unit{})
	}
	return err
}
func (r *rig) Withdraw(_ context.Context, before caddy.State, app string) (caddy.Result, error) {
	err := r.hit("withdraw_route")
	if err != nil {
		return caddy.Result{Outcome: caddy.Unchanged}, err
	}
	next := caddy.State{Generation: before.Generation + 1, Files: maps.Clone(before.Files), Sites: maps.Clone(before.Sites)}
	delete(next.Files, app+".caddy")
	delete(next.Sites, app+".caddy")
	r.facts.Routing = next
	r.facts.Input.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: next.Generation, Files: []target.CaddyFile{}})
	return caddy.Result{Outcome: caddy.Applied, Next: next}, nil
}
func (r *rig) SettleWithdrawal(context.Context, caddy.State, caddy.State, string) error {
	return r.hit("withdraw_route")
}
func (r *rig) RetireApp(context.Context, string, string, string) error {
	err := r.hit("retire_app")
	if err != nil {
		return err
	}
	r.committed = true
	r.hasRelease = false
	r.facts.Input.State.Releases = []plan.CurrentRelease{}
	r.facts.Input.State.Generation++
	r.facts.Input.Snapshot.Generation = target.Known(r.facts.Input.State.Generation)
	return nil
}
func (r *rig) AppRetired(context.Context, string, string, string) (bool, error) {
	return r.committed, nil
}

func TestRemoveEffectOrderAndNoDataOrSecretDeletion(t *testing.T) {
	r := newRemoveRig(t)
	if err := r.executor.Run(context.Background(), "operation", r.plan, r.desired); err != nil {
		t.Fatal(err)
	}
	if r.state != Succeeded || !r.committed || r.hasRelease || !reflect.DeepEqual(r.effects, []string{"withdraw_route", "stop_unit", "remove_unit", "reload_units", "retire_app"}) {
		t.Fatal(r.state, r.effects, r.committed)
	}
	if err := r.executor.Run(context.Background(), "operation", r.plan, r.desired); result.Classify(err).Code() != result.Conflict {
		t.Fatalf("stale applied twice: %v", err)
	}
}
func TestRemoveFreshnessAndPersistentRefusalBeforeEffects(t *testing.T) {
	r := newRemoveRig(t)
	r.facts.Input.State.Generation++
	r.facts.Input.Snapshot.Generation = target.Known(r.facts.Input.State.Generation)
	if err := r.executor.Run(context.Background(), "operation", r.plan, r.desired); result.Classify(err).Code() != result.Conflict || len(r.events) != 0 || len(r.effects) != 0 {
		t.Fatal(err, r.events, r.effects)
	}
	r = newRemoveRig(t)
	r.failStep = "verify_remove"
	if err := r.executor.Run(context.Background(), "operation", r.plan, r.desired); !errors.Is(err, plan.ErrPersistentData) || len(r.effects) != 0 || r.state != Failed {
		t.Fatal(err, r.effects, r.state)
	}
}
func TestRemoveNoOpNeedsNoEffectAdapters(t *testing.T) {
	r := newRig(t, false)
	var err error
	r.plan, err = plan.BuildRemove(r.facts.Input)
	if err != nil {
		t.Fatal(err)
	}
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	r.executor.Podman = nil
	r.executor.Systemd = nil
	r.executor.Routes = nil
	r.executor.Units = nil
	if err = r.executor.Run(context.Background(), "operation", r.plan, r.desired); err != nil || r.state != Succeeded || len(r.effects) != 0 {
		t.Fatal(err, r.state, r.effects)
	}
}
func TestRemoveFailureNeverRestartsOrDeletesUncertainWriter(t *testing.T) {
	for _, step := range []string{"withdraw_route", "stop_unit", "remove_unit", "reload_units", "retire_app"} {
		t.Run(step, func(t *testing.T) {
			r := newRemoveRig(t)
			r.failStep = step
			if err := r.executor.Run(context.Background(), "operation", r.plan, r.desired); err == nil || r.state != RecoveryRequired {
				t.Fatal(err, r.state)
			}
			if r.effects[len(r.effects)-1] != step {
				t.Fatal("continued after failure", r.effects)
			}
		})
	}
	r := newRemoveRig(t)
	r.executor.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
		return podman.ContainerState{Running: true, Status: "running"}, nil
	}
	if err := r.executor.Run(context.Background(), "operation", r.plan, r.desired); err == nil || r.state != RecoveryRequired {
		t.Fatal(err, r.state)
	}
	for _, effect := range r.effects {
		if effect == "remove_unit" || effect == "retire_app" {
			t.Fatal("removed with live container")
		}
	}
}

func TestRemoveUnknownEffectsReadBackWithoutBlindRetry(t *testing.T) {
	for _, step := range []string{"withdraw_route", "stop_unit", "remove_unit", "reload_units", "retire_app"} {
		t.Run(step, func(t *testing.T) {
			r := newRemoveRig(t)
			r.unknownStep = step
			reads := 0
			r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { reads++; return r.facts, nil })
			err := r.executor.Run(context.Background(), "operation", r.plan, r.desired)
			if err == nil || r.state != RecoveryRequired {
				t.Fatal(err, r.state)
			}
			count := 0
			for _, effect := range r.effects {
				if effect == step {
					count++
				}
			}
			if count != 1 || r.effects[len(r.effects)-1] != step {
				t.Fatal("retried or continued uncertain effect", r.effects)
			}
			if (step == "withdraw_route" || step == "remove_unit") && reads != 2 {
				t.Fatal("missing fresh artifact read-back", reads)
			}
			unknown := false
			for _, event := range r.events {
				var payload ops.StepPayload
				_ = json.Unmarshal(event.Payload, &payload)
				if payload.Step == step && payload.Outcome == "unknown" {
					unknown = true
				}
			}
			if !unknown {
				t.Fatal("unknown effect not journaled")
			}
		})
	}
}
