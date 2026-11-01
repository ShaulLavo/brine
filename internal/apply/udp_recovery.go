package apply

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/systemd"
)

func udpCompensationEvents(events []Event) bool {
	for _, event := range events {
		var step stepPayload
		if event.Kind == "step" && json.Unmarshal(event.Payload, &step) == nil && slices.Contains([]string{"revert_udp", "rollback_udp_check", "rollback_udp"}, step.Step) {
			return true
		}
	}
	return false
}

func udpCompensationPrefix(events []Event, p plan.Plan, d policy.Desired) (stepPayload, string, map[string]bool, bool) {
	forward := deploymentSteps(p, d)
	index := 0
	boundary := ""
	completed := map[string]bool{}
	var last stepPayload
	compensation := false
	var path []string
	rollbackIndex := 0
	for _, event := range events {
		if event.Kind != "step" {
			continue
		}
		if ops.ValidateEvent(event) != nil || json.Unmarshal(event.Payload, &last) != nil {
			return last, boundary, completed, false
		}
		rollbackStep := slices.Contains([]string{"rollback_quiesce", "check_compatibility", "rollback_route", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_udp_check", "rollback_udp", "revert_udp"}, last.Step)
		if rollbackStep && !compensation {
			path = udpCompensationPath(p, forward, boundary)
		}
		compensation = compensation || rollbackStep
		valid := true
		if !compensation {
			index, boundary, valid = udpForwardStep(forward, index, last)
		}
		if !valid {
			return last, boundary, completed, false
		}
		if !compensation {
			continue
		}
		if rollbackIndex >= len(path) || last.Step != path[rollbackIndex] {
			return last, boundary, completed, false
		}
		if last.Outcome == "completed" {
			completed[last.Step] = true
			rollbackIndex++
		}
	}
	return last, boundary, completed, compensation
}

func (e *Executor) inspectUDPCompensation(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, events []Event, r Recovery) (Recovery, error) {
	if op.State.IsTerminal() || p.Hash != op.PlanID || !desiredMatches(p, d) || len(p.Conflicts) != 0 || e.Facts == nil || e.Releases == nil || e.Plans == nil || e.UDPReservations == nil {
		return r, nil
	}
	last, boundary, completed, valid := udpCompensationPrefix(events, p, d)
	r.Step = last.Step
	if !valid || !slices.Contains([]string{"revert_udp", "rollback_udp_check", "rollback_udp"}, last.Step) {
		return r, nil
	}
	probe, cancel := context.WithTimeout(ctx, e.effectTimeout())
	defer cancel()
	x := &execution{executor: e, id: op.ID, plan: p, desired: d, state: op.State, writerStartOwners: r.resolutionOwners, udpPreparationIntent: true, recoveryRollback: completed}
	r.execution = x
	var err error
	x.facts, err = e.Facts.Read(probe)
	if err != nil {
		return r, err
	}
	if x.facts.Input.Desired.PolicyHash != p.PolicyHash || p.ObservedGeneration.Value == nil || x.facts.Input.State.Generation != *p.ObservedGeneration.Value || x.facts.Input.State.Target != p.Target {
		return r, nil
	}
	x.previous, x.hasPrevious, err = e.Releases.CurrentRelease(probe, p.App)
	if err != nil {
		return r, err
	}
	if (p.Kind == plan.Create) == x.hasPrevious {
		return r, nil
	}
	if x.hasPrevious {
		previous, d, err := e.Plans.LoadPlan(probe, x.previous.PlanID)
		if err != nil || previous.Hash != x.previous.PlanID || !desiredMatches(previous, d) {
			return r, err
		}
		x.previousDesired = d
	}
	state, err := e.UDPReservations.UDPPreparation(probe, op.ID, p)
	if err != nil {
		return r, err
	}
	if !slices.Contains([]string{"prepared", "unprepared", "reverted"}, state) {
		return r, nil
	}
	forward := deploymentSteps(p, d)
	at := slices.Index(forward, boundary)
	if at < 0 {
		return r, nil
	}
	if last.Step == "revert_udp" {
		if at >= slices.Index(forward, "quiesce_old") {
			return r, nil
		}
		if state == "reverted" || state == "unprepared" {
			completed["revert_udp"] = true
		}
		r.Action = RestorePrevious
		return r, nil
	}
	if e.Units == nil || e.Systemd == nil || e.Podman == nil || e.Health == nil || e.UDPChecks == nil {
		return r, nil
	}
	x.quiesced = x.hasPrevious && at >= slices.Index(forward, "quiesce_old")
	x.installed = at >= slices.Index(forward, "install_unit")
	x.started = at >= slices.Index(forward, "start_unit")
	if x.started && !completed["rollback_quiesce"] || x.installed && (!completed["rollback_unit"] || !completed["rollback_reload"]) || x.hasPrevious && (!completed["rollback_start"] || !completed["rollback_check"]) || x.started && x.hasPrevious && !completed["check_compatibility"] {
		return r, nil
	}
	x.unit, err = quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
	if err != nil {
		return r, err
	}
	x.service, err = systemd.ParseUnit(p.App + ".service")
	if err != nil {
		return r, err
	}
	hash, known := observedUnitHash(x.facts, p.App, x.unit.Name())
	if !known || hash != x.previousUnitHash() {
		return r, nil
	}
	writer := x.waitWriter(probe)
	if x.hasPrevious && writer != writerRunning || !x.hasPrevious && writer != writerStopped {
		return r, nil
	}
	if x.hasPrevious && (x.previous.CaddyGeneration != x.facts.Routing.Generation || x.previous.CaddyFile.Hash != x.facts.Routing.Files[x.previous.CaddyFile.Name]) {
		return r, nil
	}
	// Repeat the physical proof even after a completed proof or reversion receipt.
	delete(completed, "rollback_udp_check")
	if state == "reverted" {
		completed["rollback_udp"] = true
	}
	r.Action = RestorePrevious
	return r, nil
}

func lifecycleSteps(p plan.Plan) []string {
	steps := []string{"preflight"}
	if p.Lifecycle != plan.StartApp {
		steps = append(steps, "stop_unit")
	}
	if p.Lifecycle != plan.StopApp {
		steps = append(steps, "reload_units", "start_unit", "check_direct", "check_udp")
	}
	return steps
}
func (e *Executor) inspectUDPLifecycle(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, events []Event, r Recovery) (Recovery, error) {
	if op.State.IsTerminal() || p.Hash != op.PlanID || !desiredMatches(p, d) || len(p.Conflicts) != 0 || e.Facts == nil || e.Releases == nil || e.UDPChecks == nil {
		return r, nil
	}
	steps := lifecycleSteps(p)
	index := 0
	var last stepPayload
	for _, event := range events {
		if event.Kind != "step" {
			continue
		}
		if ops.ValidateEvent(event) != nil || json.Unmarshal(event.Payload, &last) != nil || index >= len(steps) || last.Step != steps[index] {
			return r, nil
		}
		r.Step = last.Step
		if last.Outcome == "completed" {
			r.completed[last.Step] = true
			index++
		} else if last.Outcome != "intent" && last.Outcome != "unknown" {
			return r, nil
		}
	}
	if r.Step == "" {
		return r, nil
	}
	probe, cancel := context.WithTimeout(ctx, e.effectTimeout())
	defer cancel()
	x := &execution{executor: e, id: op.ID, plan: p, desired: d, state: op.State, writerStartOwners: r.resolutionOwners}
	var err error
	x.facts, err = e.Facts.Read(probe)
	if err != nil {
		return r, err
	}
	if p.ObservedGeneration.Value == nil || x.facts.Input.State.Generation != *p.ObservedGeneration.Value || x.facts.Input.State.Target != p.Target || x.facts.Input.Desired.PolicyHash != p.PolicyHash {
		return r, nil
	}
	x.previous, x.hasPrevious, err = e.Releases.CurrentRelease(probe, p.App)
	if err != nil || !x.hasPrevious {
		return r, err
	}
	if e.Plans == nil {
		return r, nil
	}
	_, x.previousDesired, err = e.Plans.LoadPlan(probe, x.previous.PlanID)
	if err != nil {
		return r, err
	}
	if !reflect.DeepEqual(d, x.previousDesired) {
		return r, nil
	}
	hash, known := observedUnitHash(x.facts, p.App, p.App+".container")
	if !known || hash != x.previousUnitHash() {
		return r, nil
	}
	x.service, err = systemd.ParseUnit(p.App + ".service")
	if err != nil {
		return r, err
	}
	r.execution = x
	if !r.completed[r.Step] && !x.resolveUDPLifecycleBoundary(probe, &r) {
		return r, nil
	}
	if r.Step == "stop_unit" && p.Lifecycle == plan.StopApp && x.waitWriter(probe) != writerStopped {
		return r, nil
	}
	r.Action = ResumeForward
	return r, nil
}

func udpCompensationPath(p plan.Plan, forward []string, boundary string) []string {
	at := slices.Index(forward, boundary)
	if at < slices.Index(forward, "quiesce_old") {
		return []string{"revert_udp"}
	}
	path := []string{}
	if at >= slices.Index(forward, "start_unit") {
		path = append(path, "rollback_quiesce")
		if p.Kind != plan.Create {
			path = append(path, "check_compatibility")
		}
	}
	if at >= slices.Index(forward, "publish_route") {
		path = append(path, "rollback_route")
	}
	if at >= slices.Index(forward, "install_unit") {
		path = append(path, "rollback_unit", "rollback_reload")
	}
	if p.Kind != plan.Create {
		path = append(path, "rollback_start", "rollback_check")
	}
	path = append(path, "rollback_udp_check")
	if udpPreparationNeeded(p) {
		path = append(path, "rollback_udp")
	}
	return path
}

func udpForwardStep(steps []string, index int, last stepPayload) (int, string, bool) {
	if index >= len(steps) || last.Step != steps[index] {
		return index, last.Step, false
	}
	if last.Outcome == "completed" {
		index++
	}
	return index, last.Step, true
}
func (x *execution) resolveUDPLifecycleBoundary(ctx context.Context, r *Recovery) bool {
	switch r.Step {
	case "preflight", "check_direct", "check_udp":
		return true
	case "start_unit", "stop_unit":
		if x.reconcileUnknown(ctx, r.Step) != applied {
			return false
		}
		r.completed[r.Step] = true
		r.resolved = true
		return true
	default:
		return false
	}
}
