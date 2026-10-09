package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

var removeSteps = []string{"preflight", "withdraw_route", "stop_unit", "remove_unit", "reload_units", "retire_app"}

func (e *Executor) inspectRemoveRecovery(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, events []Event, r Recovery) (Recovery, error) {
	if op.State.IsTerminal() || p.Hash != op.PlanID || !desiredMatches(p, d) || len(p.Conflicts) != 0 || e.Facts == nil || e.Releases == nil {
		return r, nil
	}
	if p.Kind == plan.NoOp {
		return e.inspectNoOpRemoveRecovery(ctx, op, p, d, events, r)
	}
	if op.State.IsTerminal() || p.Hash != op.PlanID || !desiredMatches(p, d) || p.Kind != plan.Update || p.Removal == nil || len(p.Removal.Units) != 1 || len(p.Conflicts) != 0 || e.Facts == nil || e.Releases == nil || e.Podman == nil || e.Systemd == nil {
		return r, nil
	}
	units, ok := e.Units.(RemovalUnits)
	if !ok {
		return r, nil
	}
	if _, ok = e.Routes.(RemovalRoutes); !ok {
		return r, nil
	}
	store, ok := e.Releases.(RetirementStore)
	if !ok {
		return r, nil
	}
	index := 0
	var last stepPayload
	for _, event := range events {
		if event.Kind != "step" {
			continue
		}
		if json.Unmarshal(event.Payload, &last) != nil || index >= len(removeSteps) || last.Step != removeSteps[index] {
			return r, nil
		}
		r.Step = last.Step
		switch last.Outcome {
		case "completed":
			r.completed[last.Step] = true
			index++
		case "intent", "unknown":
		default:
			return r, nil
		}
	}
	r.unknownBoundary = last.Outcome == "intent"
	if r.Step == "" {
		return r, nil
	}
	x := &execution{executor: e, id: op.ID, plan: p, desired: d, state: op.State}
	r.execution = x
	evidence, cancel := context.WithTimeout(ctx, e.effectTimeout())
	defer cancel()
	var err error
	x.facts, err = e.Facts.Read(evidence)
	if err != nil {
		return r, err
	}
	if x.facts.Input.Desired.PolicyHash != p.PolicyHash || x.facts.Input.Desired.PolicyVersion != p.PolicyVersion || !reflect.DeepEqual(x.facts.Input.State.Target, p.Target) {
		return r, nil
	}
	retired, err := store.AppRetired(evidence, op.ID, p.App, p.Removal.ReleaseID)
	if err != nil {
		return r, err
	}
	for _, owner := range r.resolutionOwners {
		if retired {
			break
		}
		retired, err = store.AppRetired(evidence, owner, p.App, p.Removal.ReleaseID)
		if err != nil {
			return r, err
		}
	}
	if p.ObservedGeneration.Value == nil {
		return r, nil
	}
	generation := *p.ObservedGeneration.Value
	if retired {
		generation++
	}
	if x.facts.Input.State.Generation != generation {
		return r, nil
	}
	expected := slices.Clone(p.Removal.Releases)
	if retired {
		expected = slices.DeleteFunc(expected, func(r plan.ReleaseIdentity) bool { return r.App == p.App })
	}
	observed := []plan.ReleaseIdentity{}
	for _, release := range x.facts.Input.State.Releases {
		observed = append(observed, plan.ReleaseIdentity{App: release.App, ID: release.ID})
	}
	slices.SortFunc(observed, func(a, b plan.ReleaseIdentity) int {
		if a.App < b.App {
			return -1
		}
		if a.App > b.App {
			return 1
		}
		return 0
	})
	if !reflect.DeepEqual(expected, observed) {
		return r, nil
	}
	x.previous, x.hasPrevious, err = e.Releases.CurrentRelease(evidence, p.App)
	if err != nil {
		return r, err
	}
	if !retired && (!x.hasPrevious || x.previous.ID != p.Removal.ReleaseID || !reflect.DeepEqual(x.previous.Units, p.Removal.Units) || x.previous.CaddyFile != p.Removal.Route) || retired && x.hasPrevious {
		return r, nil
	}
	x.service, err = systemd.ParseUnit(p.App + ".service")
	if err != nil {
		return r, nil
	}
	if x.facts.Input.Snapshot.Apps.Value == nil {
		return r, nil
	}
	for _, app := range *x.facts.Input.Snapshot.Apps.Value {
		if app.Name != p.App {
			continue
		}
		if app.QuadletUnits.Value == nil || app.QuadletUnits.Status != target.KnownStatus || len(*app.QuadletUnits.Value) > 1 {
			return r, nil
		}
		for _, unit := range *app.QuadletUnits.Value {
			if unit != p.Removal.Units[0] {
				return r, nil
			}
		}
	}
	unit := p.Removal.Units[0]
	unitHash, known := observedUnitHash(x.facts, p.App, unit.Name)
	if !known || unitHash != "" && unitHash != unit.Hash {
		return r, nil
	}
	if err = units.VerifyRemove(evidence, unit.Name, unit.Hash); err != nil {
		return r, nil
	}
	route := removalRouteState(p, x.facts)
	if route == unresolved {
		return r, nil
	}
	boundary := slices.Index(removeSteps, r.Step)
	if route == notApplied && boundary > slices.Index(removeSteps, "withdraw_route") || unitHash == "" && boundary < slices.Index(removeSteps, "remove_unit") {
		return r, nil
	}
	if route == applied && r.Step == "preflight" {
		return r, nil
	}
	writer := x.waitWriter(evidence)
	if writer != writerStopped && writer != writerRunning {
		return r, nil
	}
	if boundary >= slices.Index(removeSteps, "stop_unit") {
		if writer != writerStopped && (r.Step != "stop_unit" || r.completed["stop_unit"] || writer != writerRunning) {
			return r, nil
		}
		if writer == writerStopped && r.Step == "stop_unit" && !r.completed["stop_unit"] {
			r.completed["stop_unit"] = true
			r.resolved = true
		}
	}
	if boundary >= slices.Index(removeSteps, "remove_unit") && unitHash != "" && (r.Step != "remove_unit" || r.completed["remove_unit"]) {
		return r, nil
	}
	if boundary >= slices.Index(removeSteps, "retire_app") && retired {
		r.completed["retire_app"] = true
		r.resolved = last.Outcome != "completed"
		r.Action = FinishSucceeded
		return r, nil
	}
	if retired {
		return r, nil
	}
	// Disk is not a reload receipt. An interrupted withdrawal gets a freshly
	// validated settlement reload before any writer stop, never inferred success.
	if r.Step == "withdraw_route" && !r.completed["withdraw_route"] && route == applied {
		r.resolved = false
	}
	// File deletion is content-addressed and directory-synced by Remove. Re-enter
	// it only after exact absence/presence read-back; daemon-reload is convergent.
	if r.Step == "preflight" && !r.completed["preflight"] {
		r.completed["preflight"] = true
		r.resolved = true
	}
	r.Action = ResumeForward
	return r, nil
}

// A no-op has no removal payload or runtime adapters. Its only boundary is
// read-only preflight. Fresh replanning must still prove the same app absence,
// policy, target and generation before completing the interrupted receipt.
func (e *Executor) inspectNoOpRemoveRecovery(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, events []Event, r Recovery) (Recovery, error) {
	if op.State != Preflight || p.Removal != nil || len(p.Changes) != 0 {
		return r, nil
	}
	completed := false
	for _, event := range events {
		if event.Kind != "step" {
			continue
		}
		var step stepPayload
		if json.Unmarshal(event.Payload, &step) != nil || step.Step != "preflight" || completed {
			return r, nil
		}
		switch step.Outcome {
		case "intent", "unknown":
		case "completed":
			completed = true
		default:
			return r, nil
		}
		r.Step = step.Step
		r.unknownBoundary = step.Outcome == "intent"
	}
	if r.Step == "" {
		return r, nil
	}
	evidence, cancel := context.WithTimeout(ctx, e.effectTimeout())
	defer cancel()
	facts, err := e.Facts.Read(evidence)
	if err != nil {
		return r, err
	}
	fresh, err := plan.BuildRemove(facts.Input)
	if err != nil || fresh.Kind != plan.NoOp {
		return r, nil
	}
	before, err := p.CanonicalBytes()
	if err != nil {
		return r, nil
	}
	after, err := fresh.CanonicalBytes()
	if err != nil || !bytes.Equal(before, after) {
		return r, nil
	}
	r.execution = &execution{executor: e, id: op.ID, plan: p, desired: d, facts: facts, state: op.State}
	r.resolved = !completed
	r.Action = FinishSucceeded
	return r, nil
}
