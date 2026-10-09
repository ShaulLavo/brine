package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/podman"
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

type resolutionJournal struct {
	*rig
	records map[string]Operation
}

func (j resolutionJournal) GetOperation(ctx context.Context, id string) (Operation, error) {
	if op, ok := j.records[id]; ok {
		return op, nil
	}
	return j.rig.GetOperation(ctx, id)
}

func repeatedResolution(r *rig, rootID string) (Operation, Operation) {
	root := Operation{ID: rootID, Kind: ops.Deploy, PlanID: r.plan.Hash, State: RecoveryRequired}
	middle := Operation{ID: "middle", Kind: ops.Resolve, RecoveryOf: rootID, PlanID: r.plan.Hash, State: RecoveryRequired}
	source := Operation{ID: "latest", Kind: ops.Resolve, RecoveryOf: middle.ID, PlanID: r.plan.Hash, State: RecoveryRequired}
	r.executor.Journal = resolutionJournal{rig: r, records: map[string]Operation{root.ID: root, middle.ID: middle, source.ID: source}}
	return source, Operation{ID: "successor", Kind: ops.Resolve, RecoveryOf: source.ID, PlanID: r.plan.Hash, State: Queued}
}

func TestRepeatedResolutionRecognizesCommittedAncestor(t *testing.T) {
	for _, owner := range []string{"operation-1", "middle", "latest"} {
		t.Run(owner, func(t *testing.T) {
			r := newRig(t, false)
			configureSettledRecoveryWriter(r)
			if err := r.run(); err != nil {
				t.Fatal(err)
			}
			r.release.ID = owner
			r.state = Queued
			r.effects = nil
			r.events = nil
			observeCommittedRelease(r)
			r.facts.Routing.Generation = r.release.CaddyGeneration
			r.facts.Routing.Files = map[string]string{r.release.CaddyFile.Name: r.release.CaddyFile.Hash}
			r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
			source, successor := repeatedResolution(r, "operation-1")
			events := recoveryEvents(forwardSteps...)
			assessment, err := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, events)
			if err != nil || assessment.Action != FinishSucceeded {
				t.Fatal(assessment, err)
			}
			if err = r.executor.Recover(context.Background(), assessment); err != nil || r.state != Succeeded || len(r.effects) != 0 {
				t.Fatal(err, r.state, r.effects)
			}
		})
	}
}

func TestRepeatedResolutionRecognizesRetiredAncestor(t *testing.T) {
	for _, owner := range []string{"earlier", "middle", "latest"} {
		t.Run(owner, func(t *testing.T) {
			r := newRemoveRig(t)
			for _, step := range removeSteps[1:] {
				applyRemoveBoundary(t, r, step)
			}
			r.effects = nil
			r.state = Queued
			r.executor.Releases = sourceRetirementStore{rig: r, sourceID: owner}
			source, successor := repeatedResolution(r, "earlier")
			events := recoveryEvents(removeSteps...)
			assessment, err := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, events)
			if err != nil || assessment.Action != FinishSucceeded {
				t.Fatal(assessment, err)
			}
			if err = r.executor.Recover(context.Background(), assessment); err != nil || r.state != Succeeded || len(r.effects) != 0 {
				t.Fatal(err, r.state, r.effects)
			}
		})
	}
}

func TestRepeatedResolutionRefusesInvalidAncestry(t *testing.T) {
	for _, fault := range []string{"cycle", "plan", "app", "terminal", "kind", "limit"} {
		t.Run(fault, func(t *testing.T) {
			r := newRemoveRig(t)
			source, successor := repeatedResolution(r, "earlier")
			journal := r.executor.Journal.(resolutionJournal)
			parent := journal.records["middle"]
			switch fault {
			case "cycle":
				parent.RecoveryOf = source.ID
			case "plan":
				parent.PlanID = "foreign-plan"
			case "app":
				parent.App = "foreign-app"
			case "terminal":
				parent.State = Succeeded
			case "kind":
				parent.Kind = ops.SecretSet
			case "limit":
				parent.RecoveryOf = "chain-0"
				for i := 0; i < 65; i++ {
					id := fmt.Sprintf("chain-%d", i)
					journal.records[id] = Operation{ID: id, Kind: ops.Resolve, RecoveryOf: fmt.Sprintf("chain-%d", i+1), PlanID: r.plan.Hash, State: RecoveryRequired}
				}
			}
			journal.records[parent.ID] = parent
			r.executor.Journal = journal
			assessment, err := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, recoveryEvents("preflight"))
			if err != nil || assessment.Action != RequireRecovery || len(r.effects) != 0 {
				t.Fatal(assessment, err, r.effects)
			}
		})
	}
}

func healthBoundaryResolution(t *testing.T) (*rig, Operation, Operation, []Event) {
	t.Helper()
	r := newRig(t, true)
	r.state = Queued
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	unit, err := quadlet.Render(r.desired, r.plan, *r.plan.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits = target.Known([]target.Unit{{Name: unit.Name(), Hash: unit.Hash()}})
	manager := r.executor.Systemd.(*systemd.Fake)
	manager.ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
		return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
	}
	manager.JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return false, nil }
	r.executor.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
		return podman.ContainerState{Running: true, Status: "running"}, nil
	}
	events := recoveryEvents(forwardSteps[:10]...)
	events[len(events)-1].Payload, _ = json.Marshal(ops.StepPayload{Step: "check_direct", Outcome: "completed"})
	source := Operation{ID: "earlier", Kind: ops.Deploy, PlanID: r.plan.Hash, State: RecoveryRequired}
	successor := Operation{ID: "operation", Kind: ops.Resolve, RecoveryOf: source.ID, PlanID: r.plan.Hash, State: Queued}
	return r, source, successor, events
}

func TestResolveHealthBoundaryRefusesForeignUnitBeforeStopping(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(fmt.Sprint(known), func(t *testing.T) {
			r, source, successor, events := healthBoundaryResolution(t)
			if known {
				(*(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits.Value)[0].Hash = "sha256:foreign"
			} else {
				(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits = target.Observation[[]target.Unit]{}
			}
			assessment, err := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, events)
			if err != nil || assessment.Action != RequireRecovery || len(r.effects) != 0 || !r.active {
				t.Fatalf("assessment=%+v err=%v effects=%v active=%v", assessment, err, r.effects, r.active)
			}
		})
	}
}

func TestResolveHealthBoundaryRefusesUnsettledManagerJob(t *testing.T) {
	for _, outcome := range []string{"intent", "completed"} {
		t.Run(outcome, func(t *testing.T) {
			r, source, successor, events := healthBoundaryResolution(t)
			r.executor.EffectTimeout = 25 * time.Millisecond
			probes := 0
			r.executor.Systemd.(*systemd.Fake).JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { probes++; return true, nil }
			events[len(events)-1].Payload, _ = json.Marshal(ops.StepPayload{Step: "check_direct", Outcome: outcome})
			assessment, err := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, events)
			if err != nil || assessment.Action != RequireRecovery || probes == 0 || len(r.effects) != 0 || !r.active {
				t.Fatalf("action=%s err=%v probes=%d effects=%v active=%v", assessment.Action, err, probes, r.effects, r.active)
			}
		})
	}
}
