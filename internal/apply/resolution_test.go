package apply

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestResolvePiTerminalRemoval(t *testing.T) {
	r := newRemoveRig(t)
	applyRemoveBoundary(t, r, "withdraw_route")
	applyRemoveBoundary(t, r, "stop_unit")
	events := recoveryEvents("preflight", "withdraw_route", "stop_unit")
	events[len(events)-1].Payload, _ = json.Marshal(ops.StepPayload{Step: "stop_unit", Outcome: "unknown", Code: "interrupted"})
	r.effects = nil
	r.events = events
	r.state = Queued
	source := Operation{ID: "earlier", Kind: ops.Deploy, PlanID: r.plan.Hash, State: RecoveryRequired}
	successor := Operation{ID: "operation", Kind: ops.Resolve, RecoveryOf: source.ID, PlanID: r.plan.Hash, State: Queued}
	assessment, err := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, events)
	if err != nil || assessment.Action != ResumeForward {
		t.Fatal(assessment, err)
	}
	if err = r.executor.Recover(context.Background(), assessment); err != nil || r.state != Succeeded || r.hasRelease {
		t.Fatal(err, r.state, r.effects)
	}
	for _, effect := range r.effects {
		if effect == "withdraw_route" || effect == "stop_unit" {
			t.Fatal("replayed settled effect", effect)
		}
	}
}

func TestResolveDeployAndUpdateInstalledCandidateRollsBack(t *testing.T) {
	for _, previous := range []bool{false, true} {
		t.Run(map[bool]string{false: "deploy", true: "update"}[previous], func(t *testing.T) {
			r := newRig(t, previous)
			r.state = Queued
			r.active = false
			r.intent = "install_unit"
			r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
			unit, err := quadlet.Render(r.desired, r.plan, *r.plan.Image.ManifestDigest.Value)
			if err != nil {
				t.Fatal(err)
			}
			if !previous {
				r.facts.Input.Snapshot.Apps = target.Known([]target.App{{Name: r.plan.App}})
			}
			(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits = target.Known([]target.Unit{{Name: unit.Name(), Hash: unit.Hash()}})
			manager := r.executor.Systemd.(*systemd.Fake)
			manager.ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
				return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
			}
			manager.JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return false, nil }
			events := recoveryEvents(forwardSteps[:7]...)
			events[len(events)-1].Payload, _ = json.Marshal(ops.StepPayload{Step: "install_unit", Outcome: "unknown", Code: "interrupted"})
			source := Operation{ID: "earlier", Kind: ops.Deploy, PlanID: r.plan.Hash, State: RecoveryRequired}
			successor := Operation{ID: "operation", Kind: ops.Resolve, RecoveryOf: source.ID, PlanID: r.plan.Hash, State: Queued}
			assessment, err := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, events)
			if err != nil || assessment.Action != RestorePrevious {
				t.Fatal(assessment, err)
			}
			err = r.executor.Recover(context.Background(), assessment)
			var failure *Error
			if !errors.As(err, &failure) || r.state != RolledBack {
				t.Fatal(err, r.state, r.effects)
			}
			for _, effect := range r.effects {
				if effect == "install_unit" || effect == "pull_image" || effect == "publish_route" {
					t.Fatal("replayed forward effect", effect)
				}
			}
		})
	}
}

func TestResolveRefusesAmbiguousLiveRemoval(t *testing.T) {
	r := newRemoveRig(t)
	applyRemoveBoundary(t, r, "withdraw_route")
	applyRemoveBoundary(t, r, "stop_unit")
	(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits.Value = &[]target.Unit{{Name: r.plan.Removal.Units[0].Name, Hash: r.plan.Removal.Route.Hash}}
	r.effects = nil
	events := recoveryEvents("preflight", "withdraw_route", "stop_unit")
	source := Operation{ID: "earlier", Kind: ops.Deploy, PlanID: r.plan.Hash, State: RecoveryRequired}
	successor := Operation{ID: "operation", Kind: ops.Resolve, RecoveryOf: source.ID, PlanID: r.plan.Hash, State: Queued}
	assessment, err := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, events)
	if err != nil || assessment.Action != RequireRecovery || len(r.effects) != 0 {
		t.Fatal(assessment, err, r.effects)
	}
}

type sourceRetirementStore struct {
	*rig
	sourceID string
}

func (s sourceRetirementStore) AppRetired(_ context.Context, id, _, _ string) (bool, error) {
	return id == s.sourceID && s.committed, nil
}

func TestResolveRemovalAlreadyRetiredBySource(t *testing.T) {
	r := newRemoveRig(t)
	for _, step := range removeSteps[1:] {
		applyRemoveBoundary(t, r, step)
	}
	events := recoveryEvents(removeSteps...)
	events[len(events)-1].Payload, _ = json.Marshal(ops.StepPayload{Step: "retire_app", Outcome: "unknown", Code: "interrupted"})
	r.effects = nil
	r.state = Queued
	r.executor.Releases = sourceRetirementStore{rig: r, sourceID: "earlier"}
	source := Operation{ID: "earlier", Kind: ops.Deploy, PlanID: r.plan.Hash, State: RecoveryRequired}
	successor := Operation{ID: "operation", Kind: ops.Resolve, RecoveryOf: source.ID, PlanID: r.plan.Hash, State: Queued}
	assessment, err := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, events)
	if err != nil || assessment.Action != FinishSucceeded {
		t.Fatal(assessment, err)
	}
	if err = r.executor.Recover(context.Background(), assessment); err != nil || r.state != Succeeded || len(r.effects) != 0 {
		t.Fatal(err, r.state, r.effects)
	}
}
