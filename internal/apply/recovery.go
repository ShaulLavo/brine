package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sync/atomic"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type RecoveryAction string

const (
	ResumeForward   RecoveryAction = "resume"
	RestorePrevious RecoveryAction = "rollback"
	FinishSucceeded RecoveryAction = "succeeded"
	RequireRecovery RecoveryAction = "recovery_required"
)

// Recovery holds an inspected continuation, not caller-supplied replay instructions.
// InspectRecovery and Recover must run under the same host lock. A Recovery is
// single-use; callers must inspect again after any journal or adapter failure.
type Recovery struct {
	Action           RecoveryAction `json:"action"`
	Step             string         `json:"step,omitempty"`
	operation        Operation
	execution        *execution
	completed        map[string]bool
	unknownBoundary  bool
	resolved         bool
	decision         RecoveryAction
	resolutionState  State
	resolutionOwners []string
	boundary         string
	used             *atomic.Bool
}

var forwardSteps = []string{"preflight", "pull_image", "verify_image", "ensure_secrets", "stage_unit", "quiesce_old", "install_unit", "reload_units", "start_unit", "check_direct", "publish_route", "check_routed", "commit"}

// InspectRecovery never stages, installs, starts, reloads, commits or journals.
// Reload and route outcomes lack authoritative read-back in the current adapters.
// Those boundaries deliberately require human recovery, even if health is green.
func (e *Executor) InspectRecovery(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, events []Event) (Recovery, error) {
	return e.inspectRecovery(ctx, op, p, d, events, nil)
}

func (e *Executor) inspectRecovery(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, events []Event, owners []string) (r Recovery, inspectErr error) {
	r = Recovery{Action: RequireRecovery, operation: op, completed: map[string]bool{}, used: &atomic.Bool{}, resolutionOwners: owners}
	defer func() { r.decision, r.boundary = r.Action, r.Step }()
	forwardSteps := deploymentSteps(d)
	var prefixOK bool
	var refusedEffect bool
	events, refusedEffect, prefixOK = ops.InspectionPrefix(events)
	if !prefixOK {
		return r, nil
	}
	if p.Lifecycle == plan.ReviseReplica {
		return e.inspectReplicaRevisionRecovery(ctx, op, p, d, events, r)
	}
	if p.Lifecycle == plan.PrepareData {
		return e.inspectDataPreparationRecovery(ctx, op, p, d, events, r)
	}
	if p.Lifecycle == plan.RemoveApp {
		return e.inspectRemoveRecovery(ctx, op, p, d, events, r)
	}
	if op.State.IsTerminal() {
		return r, nil
	}
	if p.Hash != op.PlanID || !desiredMatches(p, d) || d.Health.StartupDeadlineSeconds < 1 || d.Health.StartupDeadlineSeconds > spec.MaxStartupDeadlineSeconds || p.Image.ManifestDigest.Value == nil || (p.Kind != plan.Create && p.Kind != plan.Update && p.Kind != plan.NoOp) || len(p.Conflicts) != 0 || e.Facts == nil || e.Releases == nil {
		return r, nil
	}
	// Only a contiguous executor prefix can authorize a continuation. Failed and
	// rollback attempts cannot be mistaken for completed forward steps.
	index := 0
	validPrefix := true
	var last stepPayload
	rollbackCompleted := map[string]bool{}
	var rollbackPath []string
	rollbackIndex := 0
	rollbackPending := false
	for _, event := range events {
		if event.Kind != "step" {
			continue
		}
		var step stepPayload
		if json.Unmarshal(event.Payload, &step) != nil {
			return r, nil
		}
		rollbackStep := slices.Contains([]string{"rollback_quiesce", "check_compatibility", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_route"}, step.Step)
		if refusedEffect && rollbackStep {
			if rollbackPath == nil {
				boundary := slices.Index(forwardSteps, r.Step)
				if boundary >= slices.Index(forwardSteps, "publish_route") || boundary < 0 {
					return r, nil
				}
				if boundary >= slices.Index(forwardSteps, "start_unit") {
					rollbackPath = append(rollbackPath, "rollback_quiesce")
					if p.Kind != plan.Create {
						rollbackPath = append(rollbackPath, "check_compatibility")
					}
				}
				if boundary >= slices.Index(forwardSteps, "install_unit") {
					rollbackPath = append(rollbackPath, "rollback_unit", "rollback_reload")
				}
				if p.Kind != plan.Create {
					rollbackPath = append(rollbackPath, "rollback_start", "rollback_check")
				}
			}
			if rollbackIndex >= len(rollbackPath) || step.Step != rollbackPath[rollbackIndex] {
				return r, nil
			}
			switch step.Outcome {
			case "completed":
				if step.Step == "check_compatibility" && !slices.Contains([]string{"stateless_compatible", "compatibility_verified"}, step.Code) {
					return r, nil
				}
				rollbackCompleted[step.Step] = true
				rollbackIndex++
				rollbackPending = false
			case "intent", "unknown":
				rollbackPending = true
			default:
				return r, nil
			}
			continue
		}
		if rollbackPath != nil {
			return r, nil
		}
		last = step
		if index >= len(forwardSteps) || last.Step != forwardSteps[index] {
			validPrefix = false
		}
		r.Step = last.Step
		switch last.Outcome {
		case "completed":
			r.completed[last.Step] = true
			index++
		case "intent", "unknown":
		default:
			validPrefix = false
		}
	}
	if rollbackPending {
		return r, nil
	}
	if r.Step == "" {
		return r, nil
	}
	if !validPrefix && op.State != RollingBack {
		return r, nil
	}
	x := &execution{executor: e, id: op.ID, plan: p, desired: d, state: op.State, recoveryRollback: rollbackCompleted, writerStartOwners: owners}
	r.execution = x
	if len(owners) > 0 {
		x.replicaOperation = owners[len(owners)-1]
	} else {
		x.replicaOperation = op.ID
	}
	var err error
	evidence, cancel := context.WithTimeout(ctx, e.effectTimeout())
	defer cancel()
	x.facts, err = e.Facts.Read(evidence)
	if err != nil {
		return r, err
	}
	if x.facts.Input.Desired.PolicyHash != p.PolicyHash || x.facts.Input.Desired.PolicyVersion != p.PolicyVersion {
		return r, nil
	}
	x.previous, x.hasPrevious, err = e.Releases.CurrentRelease(evidence, p.App)
	if err != nil {
		return r, err
	}
	// A committed operation needs exact artifact read-back, not just an ID.
	if validPrefix && x.hasPrevious && (x.previous.ID == op.ID || slices.Contains(r.resolutionOwners, x.previous.ID)) {
		if r.Step != "commit" || x.previous.PlanID != p.Hash {
			return r, nil
		}
		x.unit, err = quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
		if err != nil {
			return r, nil
		}
		candidate := x.previous
		candidate.Image, candidate.HostPort, candidate.Secrets = p.Image, p.HostPort, p.Secrets
		unitFound := false
		for _, unit := range candidate.Units {
			if unit.Name == x.unit.Name() && unit.Hash == x.unit.Hash() {
				unitFound = true
			}
		}
		if !unitFound || !committedArtifactsObserved(x.facts, candidate, p.App) || candidate.CaddyFile.Name != p.App+".caddy" || candidate.CaddyFile.Hash == "" || candidate.CaddyFile.Hash != x.facts.Routing.Files[p.App+".caddy"] || candidate.CaddyGeneration != x.facts.Routing.Generation {
			return r, nil
		}
		x.nextRelease = &candidate
		writer := x.waitWriter(evidence)
		if writer != writerStopped && writer != writerRunning {
			return r, nil
		}
		if x.reconcileUnknown(ctx, "commit") == applied {
			r.Action = FinishSucceeded
			r.resolved = last.Outcome != "completed"
		}
		return r, nil
	}
	if p.ObservedGeneration.Status != target.KnownStatus || p.ObservedGeneration.Value == nil || x.facts.Input.State.Generation != *p.ObservedGeneration.Value || !reflect.DeepEqual(p.Target, x.facts.Input.State.Target) {
		return r, nil
	}
	if (p.Kind == plan.Create && x.hasPrevious) || (p.Kind != plan.Create && !x.hasPrevious) {
		return r, nil
	}
	if x.hasPrevious {
		if e.Plans == nil {
			return r, nil
		}
		oldPlan, oldDesired, err := e.Plans.LoadPlan(evidence, x.previous.PlanID)
		if err != nil {
			return r, err
		}
		if oldPlan.Hash != x.previous.PlanID || !desiredMatches(oldPlan, oldDesired) || oldDesired.Health.StartupDeadlineSeconds < 1 || oldDesired.Health.StartupDeadlineSeconds > spec.MaxStartupDeadlineSeconds {
			return r, nil
		}
		x.previousDesired = oldDesired
	}
	x.unit, err = quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
	if err != nil {
		return r, nil
	}
	x.service, err = systemd.ParseUnit(p.App + ".service")
	if err != nil {
		return r, nil
	}
	if !validPrefix {
		// Rollback lacks a durable pre-publication route baseline. Still inspect
		// supported unknown effects, but never recreate its lost execution flags.
		x.reconcileUnknown(ctx, r.Step)
		return r, nil
	}
	if r.Step == "prepare_data" && last.Outcome != "completed" {
		if x.reconcileUnknown(evidence, r.Step) != applied {
			cursor, ok := e.PersistentData.(ReplicaRevisionRecovery)
			if !ok {
				return r, nil
			}
			ready, err := cursor.InspectReplicaRevision(evidence, x.replicaOperation, p, d)
			if err != nil || !ready {
				return r, err
			}
			x.resumeReplicaOnly = true
		}
	}
	// Completed durable outcomes establish earlier effects. The last boundary is
	// read back even when its outcome was written before the crash.
	if last.Outcome != "completed" {
		switch r.Step {
		case "preflight", "verify_image", "ensure_secrets", "check_direct", "check_routed":
			// These are read-only and can be rechecked, never replayed as mutations.
		default:
			if !x.resumeReplicaOnly {
				if x.reconcileUnknown(ctx, r.Step) != applied {
					return r, nil
				}
				r.completed[r.Step] = true
				r.resolved = true
			}
		}
	} else if slices.Contains([]string{"prepare_data", "quiesce_old", "install_unit", "start_unit"}, r.Step) && x.reconcileUnknown(ctx, r.Step) != applied {
		return r, nil
	}
	boundary := slices.Index(forwardSteps, r.Step)
	x.quiesced = x.hasPrevious && boundary >= slices.Index(forwardSteps, "quiesce_old")
	x.installed = boundary >= slices.Index(forwardSteps, "install_unit")
	x.started = boundary >= slices.Index(forwardSteps, "start_unit")
	// Journaled installation is not ownership of the artifact now on disk.
	// Prove ownership before rollback can stop its service, not in rollback_unit.
	if x.quiesced || x.installed {
		hash, known := observedUnitHash(x.facts, p.App, x.unit.Name())
		if !known || hash != x.unit.Hash() && hash != x.previousUnitHash() || x.installed && hash == "" && !rollbackCompleted["rollback_unit"] {
			return r, nil
		}
	}
	if rollbackCompleted["rollback_unit"] {
		hash, known := observedUnitHash(x.facts, p.App, x.unit.Name())
		if !known || hash != x.previousUnitHash() {
			return r, nil
		}
	}
	// A published route cannot be restored from live facts alone. The original
	// generation was not persisted by this executor, so never invent it.
	if boundary >= slices.Index(forwardSteps, "publish_route") {
		return r, nil
	}
	fresh, buildErr := plan.Build(x.facts.Input)
	original, err := p.CanonicalBytes()
	if err != nil {
		return r, nil
	}
	rebuilt, canonicalErr := fresh.CanonicalBytes()
	if len(rollbackCompleted) == 0 && buildErr == nil && canonicalErr == nil && bytes.Equal(original, rebuilt) {
		r.Action = ResumeForward
	} else if x.quiesced || x.installed {
		if !x.hasPrevious || (x.previous.CaddyGeneration == x.facts.Routing.Generation && x.previous.CaddyFile.Hash == x.facts.Routing.Files[x.previous.CaddyFile.Name]) {
			r.Action = RestorePrevious
		}
	}
	if r.Action == RestorePrevious && (e.Units == nil || e.Systemd == nil || e.Podman == nil || e.Health == nil) {
		r.Action = RequireRecovery
	}
	if r.Action != RequireRecovery {
		// A read-only last step does not fence manager jobs or prove the writer.
		// Every authorization needs a fresh, settled unit/container observation.
		writer := x.waitWriter(evidence)
		if rollbackCompleted["rollback_quiesce"] && !rollbackCompleted["rollback_start"] && writer != writerStopped || rollbackCompleted["rollback_start"] && writer != writerRunning {
			r.Action = RequireRecovery
		}
		if writer != writerStopped && writer != writerRunning || x.quiesced && !x.started && writer != writerStopped || r.Action == ResumeForward && (x.started && writer != writerRunning || !x.hasPrevious && !x.started && writer != writerStopped) {
			r.Action = RequireRecovery
		}
	}
	return r, nil
}

func (e *Executor) Recover(ctx context.Context, r Recovery) error {
	if r.execution == nil || r.execution.executor != e || r.Action != r.decision || r.Step != r.boundary || r.used == nil || !r.used.CompareAndSwap(false, true) {
		return errors.New("apply: invalid recovery assessment")
	}
	x := r.execution
	if err := x.prepareResolution(ctx, r.resolutionState); err != nil {
		return err
	}
	r.operation.State = x.state
	if r.unknownBoundary {
		if err := x.event(ctx, r.Step, "unknown", "interrupted"); err != nil {
			return err
		}
	}
	if r.resolved {
		if err := x.event(ctx, r.Step, "completed", ""); err != nil {
			return err
		}
	}
	switch r.Action {
	case ResumeForward:
		if x.plan.Lifecycle == plan.ReviseReplica {
			return x.reviseReplica(ctx, r.completed)
		}
		if x.plan.Lifecycle == plan.RemoveApp {
			return x.remove(ctx, r.completed, !r.completed["withdraw_route"] && removalRouteState(x.plan, x.facts) == applied)
		}
		return e.run(ctx, x.id, x.plan, x.desired, &r)
	case RestorePrevious:
		journal, cancel := context.WithTimeout(ctx, journalTimeout)
		defer cancel()
		payload, _ := json.Marshal(struct {
			Code string `json:"code"`
		}{"stale_plan"})
		if _, err := e.Journal.AppendEvent(journal, x.id, Event{Kind: "failure", Payload: payload}); err != nil {
			return &Error{Step: r.Step, Code: "journal_failed", Cause: err}
		}
		return x.fail(ctx, &Error{Step: r.Step, Code: "drift"})
	case FinishSucceeded:
		return x.terminal(ctx, Succeeded, nil)
	default:
		return x.terminal(ctx, RecoveryRequired, &Error{Step: r.Step, Code: "interrupted"})
	}
}

func committedArtifactsObserved(facts Facts, release Release, app string) bool {
	config := facts.Input.Snapshot.CaddyConfig
	return config.Status == target.KnownStatus && config.Value != nil && config.Value.Generation == release.CaddyGeneration && releaseArtifactsObserved(facts, release, app)
}

func releaseArtifactsObserved(facts Facts, release Release, app string) bool {
	apps := facts.Input.Snapshot.Apps
	if apps.Status != target.KnownStatus || apps.Value == nil {
		return false
	}
	var installed *target.App
	for i := range *apps.Value {
		if (*apps.Value)[i].Name == app {
			installed = &(*apps.Value)[i]
			break
		}
	}
	if installed == nil {
		return false
	}
	units := installed.QuadletUnits
	if units.Status != target.KnownStatus || units.Value == nil || len(*units.Value) != len(release.Units) {
		return false
	}
	for _, unit := range release.Units {
		hash, known := observedUnitHash(facts, app, unit.Name)
		if !known || hash != unit.Hash {
			return false
		}
	}
	image := installed.Image
	if image.Status != target.KnownStatus || image.Value == nil || image.Value.Digest != release.Image.Digest || image.Value.Platform != release.Image.Platform {
		return false
	}
	port := installed.AllocatedHostPort
	if port.Status != target.KnownStatus || port.Value == nil || *port.Value != release.HostPort {
		return false
	}
	if len(release.Secrets) > 0 {
		secrets := installed.Secrets
		if secrets.Status != target.KnownStatus || secrets.Value == nil {
			return false
		}
		for _, binding := range release.Secrets {
			found := false
			for _, secret := range *secrets.Value {
				if secret.Name == binding.VersionName && secret.ID == binding.ID {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	config := facts.Input.Snapshot.CaddyConfig
	if config.Status != target.KnownStatus || config.Value == nil {
		return false
	}
	for _, file := range config.Value.Files {
		if file == release.CaddyFile {
			return true
		}
	}
	return false
}

func deploymentSteps(d policy.Desired) []string {
	if d.Stateless() {
		return forwardSteps
	}
	index := slices.Index(forwardSteps, "stage_unit")
	steps := make([]string, 0, len(forwardSteps)+1)
	steps = append(steps, forwardSteps[:index]...)
	steps = append(steps, "prepare_data")
	return append(steps, forwardSteps[index:]...)
}
