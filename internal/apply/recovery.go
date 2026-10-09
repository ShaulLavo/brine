package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/target"
	"reflect"
	"slices"
	"sync/atomic"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/systemd"
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
	Action    RecoveryAction `json:"action"`
	Step      string         `json:"step,omitempty"`
	operation Operation
	execution *execution
	completed map[string]bool
	resolved  bool
	decision  RecoveryAction
	boundary  string
	used      *atomic.Bool
}

var forwardSteps = []string{"preflight", "pull_image", "verify_image", "ensure_secrets", "stage_unit", "quiesce_old", "install_unit", "reload_units", "start_unit", "check_direct", "publish_route", "check_routed", "commit"}

// InspectRecovery never stages, installs, starts, reloads, commits or journals.
// Reload and route outcomes lack authoritative read-back in the current adapters.
// Those boundaries deliberately require human recovery, even if health is green.
func (e *Executor) InspectRecovery(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, events []Event) (r Recovery, inspectErr error) {
	r = Recovery{Action: RequireRecovery, operation: op, completed: map[string]bool{}, used: &atomic.Bool{}}
	defer func() { r.decision, r.boundary = r.Action, r.Step }()
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
	for _, event := range events {
		if event.Kind != "step" {
			continue
		}
		if json.Unmarshal(event.Payload, &last) != nil {
			return r, nil
		}
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
	if r.Step == "" {
		return r, nil
	}
	if !validPrefix && op.State != RollingBack {
		return r, nil
	}
	x := &execution{executor: e, id: op.ID, plan: p, desired: d, state: op.State}
	r.execution = x
	var err error
	evidence, cancel := context.WithTimeout(ctx, e.effectTimeout())
	defer cancel()
	x.facts, err = e.Facts.Read(evidence)
	if err != nil {
		return r, err
	}
	x.previous, x.hasPrevious, err = e.Releases.CurrentRelease(evidence, p.App)
	if err != nil {
		return r, err
	}
	// A committed operation needs exact artifact read-back, not just an ID.
	if validPrefix && x.hasPrevious && x.previous.ID == op.ID {
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
		if !unitFound || candidate.CaddyFile.Name != p.App+".caddy" || candidate.CaddyFile.Hash == "" || candidate.CaddyFile.Hash != x.facts.Routing.Files[p.App+".caddy"] || candidate.CaddyGeneration != x.facts.Routing.Generation {
			return r, nil
		}
		x.nextRelease = &candidate
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
	// Completed durable outcomes establish earlier effects. The last boundary is
	// read back even when its outcome was written before the crash.
	if last.Outcome != "completed" {
		switch r.Step {
		case "preflight", "verify_image", "ensure_secrets", "check_direct", "check_routed":
			// These are read-only and can be rechecked, never replayed as mutations.
		default:
			if x.reconcileUnknown(ctx, r.Step) != applied {
				return r, nil
			}
			r.completed[r.Step] = true
			r.resolved = true
		}
	} else if slices.Contains([]string{"quiesce_old", "install_unit", "start_unit"}, r.Step) && x.reconcileUnknown(ctx, r.Step) != applied {
		return r, nil
	}
	boundary := slices.Index(forwardSteps, r.Step)
	x.quiesced = x.hasPrevious && boundary >= slices.Index(forwardSteps, "quiesce_old")
	x.installed = boundary >= slices.Index(forwardSteps, "install_unit")
	x.started = boundary >= slices.Index(forwardSteps, "start_unit")
	// A published route cannot be restored from live facts alone. The original
	// generation was not persisted by this executor, so never invent it.
	if boundary >= slices.Index(forwardSteps, "publish_route") {
		return r, nil
	}
	fresh, err := plan.Build(x.facts.Input)
	if err != nil {
		return r, nil
	}
	original, err := p.CanonicalBytes()
	if err != nil {
		return r, nil
	}
	rebuilt, err := fresh.CanonicalBytes()
	if err == nil && bytes.Equal(original, rebuilt) {
		r.Action = ResumeForward
	} else if x.quiesced || x.installed {
		// Rollback uses committed immutable artifacts. Before routing changed,
		// the observed original generation must still agree with the old release.
		if !x.hasPrevious || (x.previous.CaddyGeneration == x.facts.Routing.Generation && x.previous.CaddyFile.Hash == x.facts.Routing.Files[x.previous.CaddyFile.Name]) {
			r.Action = RestorePrevious
		}
	}
	return r, nil
}

func (e *Executor) Recover(ctx context.Context, r Recovery) error {
	if r.execution == nil || r.execution.executor != e || r.Action != r.decision || r.Step != r.boundary || r.used == nil || !r.used.CompareAndSwap(false, true) {
		return errors.New("apply: invalid recovery assessment")
	}
	x := r.execution
	if r.resolved {
		if err := x.event(ctx, r.Step, "completed", ""); err != nil {
			return err
		}
	}
	switch r.Action {
	case ResumeForward:
		return e.run(ctx, x.id, x.plan, x.desired, &r)
	case RestorePrevious:
		return x.fail(ctx, &Error{Step: r.Step, Code: "drift"})
	case FinishSucceeded:
		return x.terminal(ctx, Succeeded, nil)
	default:
		return x.terminal(ctx, RecoveryRequired, &Error{Step: r.Step, Code: "interrupted"})
	}
}
