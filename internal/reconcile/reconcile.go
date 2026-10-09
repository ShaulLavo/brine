// Package reconcile resolves abandoned operations without launching replacement jobs.
package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/systemd"
)

const EventPageSize = 128
const MaxEvents = 4096

type Store interface {
	TryAcquireHostLock(context.Context) (ops.Lock, error)
	AcquireLaunchLock(context.Context) (ops.Lock, error)
	ListUnfinished(context.Context) ([]ops.Operation, error)
	GetOperation(context.Context, string) (ops.Operation, error)
	EventsAfter(context.Context, string, uint64, int) ([]ops.Event, error)
	LoadPlan(context.Context, string) (plan.Plan, policy.Desired, error)
	AppendEvent(context.Context, string, ops.Event) (uint64, error)
	TransitionOperation(context.Context, string, ops.State, ops.State) error
}
type RunnerInspector interface {
	Show(context.Context, systemd.Unit) (systemd.Properties, error)
	JobPending(context.Context, systemd.Unit) (bool, error)
}

// After is a projected terminal target in a dry-run, not a health guarantee.
// A live reconcile replaces it with the actual durable executor state.
type Outcome struct {
	OperationID string    `json:"operation_id"`
	Before      ops.State `json:"before"`
	After       ops.State `json:"after"`
	Action      string    `json:"action"`
	Code        string    `json:"code,omitempty"`
	Step        string    `json:"step,omitempty"`
}
type Report struct {
	ControlState string    `json:"control_state,omitempty"`
	DryRun       bool      `json:"dry_run"`
	Outcomes     []Outcome `json:"outcomes"`
}
type Reconciler struct {
	Store    Store
	Systemd  RunnerInspector
	Executor *apply.Executor
	// ExecutorFor binds fresh facts and policy input to each operation. It must
	// only construct adapters, never stage or execute effects. It takes priority
	// over Executor, which is useful for a single-operation host or tests.
	ExecutorFor  func(context.Context, ops.Operation, plan.Plan, policy.Desired) (*apply.Executor, error)
	ExcludeID    string // Trusted detached recovery runner; never requester-controlled.
	LockTimeout  time.Duration
	ProbeTimeout time.Duration
}

func (r Reconciler) Reconcile(ctx context.Context) (Report, error) { return r.withLock(ctx, false) }
func (r Reconciler) DryRun(ctx context.Context) (Report, error)    { return r.withLock(ctx, true) }
func (r Reconciler) withLock(ctx context.Context, dry bool) (report Report, err error) {
	if r.Store == nil || r.Systemd == nil {
		return report, errors.New("reconcile: dependencies missing")
	}
	bound := r.LockTimeout
	if bound <= 0 {
		bound = 100 * time.Millisecond
	}
	wait, cancel := context.WithTimeout(ctx, bound)
	launch, lock, err := r.acquireLocks(wait)
	cancel()
	if err != nil {
		return report, errors.Join(ops.ErrLockUnavailable, err)
	}
	defer func() {
		if launch != nil {
			err = errors.Join(err, launch.Release())
		}
	}()
	defer func() { err = errors.Join(err, lock.Release()) }()
	// Only launch-state settlement needs the fence. Do not hold it through a
	// potentially long resumed deployment or rollback.
	report, err = r.reconcileUnderLocks(ctx, lock, launch, r.ExcludeID, dry, true, false)
	if err != nil {
		return report, err
	}
	releaseErr := launch.Release()
	launch = nil
	if releaseErr != nil {
		return report, releaseErr
	}
	rest, err := r.reconcileUnderLocks(ctx, lock, nil, r.ExcludeID, dry, false, true)
	report.Outcomes = append(report.Outcomes, rest.Outcomes...)
	return report, err
}

// Never wait on the host lock while holding the launch fence. Every attempt
// takes launch before host; both are released before the next attempt.
func (r Reconciler) acquireLocks(ctx context.Context) (ops.Lock, ops.Lock, error) {
	for {
		launch, err := r.Store.AcquireLaunchLock(ctx)
		if err != nil {
			return nil, nil, err
		}
		host, err := r.Store.TryAcquireHostLock(ctx)
		if err == nil {
			return launch, host, nil
		}
		if releaseErr := launch.Release(); releaseErr != nil {
			return nil, nil, errors.Join(err, releaseErr)
		}
		if !errors.Is(err, ops.ErrLockUnavailable) {
			return nil, nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// ReconcileUnderLock is for run-op after acquiring its host lock and before
// loading or executing its own operation. excludeID is that trusted runner's ID.
// The caller retains and releases the lock; this method never launches a job.
// Queued/launch-unknown records are deferred. Taking their launch fence while
// already holding the host lock would reverse the required lock order.
func (r Reconciler) ReconcileUnderLock(ctx context.Context, lock ops.Lock, excludeID string, dry bool) (Report, error) {
	return r.reconcileUnderLocks(ctx, lock, nil, excludeID, dry, false, false)
}

func (r Reconciler) reconcileUnderLocks(ctx context.Context, lock, launch ops.Lock, excludeID string, dry, launchOnly, skipLaunch bool) (Report, error) {
	report := Report{DryRun: dry, Outcomes: []Outcome{}}
	if lock == nil || r.Store == nil || r.Systemd == nil {
		return report, errors.New("reconcile: lock and dependencies required")
	}
	unfinished, err := r.Store.ListUnfinished(ctx)
	if err != nil {
		return report, err
	}
	for _, listed := range unfinished {
		if listed.ID == excludeID {
			continue
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
		// Launcher settlement may happen without the mutation lock. Always use a
		// fresh state and compare-and-set; never overwrite another runner's outcome.
		op, err := r.Store.GetOperation(ctx, listed.ID)
		if err != nil {
			return report, err
		}
		if op.State.IsTerminal() {
			continue
		}
		launchState := op.State == ops.Queued || op.State == ops.LaunchUnknown
		if (launchOnly && !launchState) || (skipLaunch && launchState) {
			continue
		}
		if launch == nil && (op.State == ops.Queued || op.State == ops.LaunchUnknown) {
			report.Outcomes = append(report.Outcomes, Outcome{OperationID: op.ID, Before: op.State, After: op.State, Action: "unchanged"})
			continue
		}
		outcome, recovery, err := r.inspect(ctx, op)
		if err != nil {
			return report, err
		}
		if !dry && outcome.Action != "running" && outcome.Action != "unchanged" {
			if recovery != nil && recovery.assessment.Action != apply.RequireRecovery {
				runErr := recovery.executor.Recover(ctx, recovery.assessment)
				current, readErr := r.Store.GetOperation(ctx, op.ID)
				if readErr != nil {
					return report, errors.Join(runErr, readErr)
				}
				outcome.After = current.State
				if current.State == ops.RecoveryRequired {
					outcome.Action, outcome.Code = "recovery_required", "recovery_required"
				}
				if runErr != nil && !current.State.IsTerminal() {
					return report, runErr
				}
			} else {
				payload, _ := json.Marshal(ops.FailurePayload{Code: outcome.Code})
				if _, err := r.Store.AppendEvent(ctx, op.ID, ops.Event{Kind: "failure", Payload: payload}); err != nil {
					return report, err
				}
				if err := r.Store.TransitionOperation(ctx, op.ID, op.State, outcome.After); err != nil {
					return report, err
				}
			}
		}
		report.Outcomes = append(report.Outcomes, outcome)
	}
	return report, nil
}

type continuation struct {
	executor   *apply.Executor
	assessment apply.Recovery
}

func (r Reconciler) inspect(ctx context.Context, op ops.Operation) (Outcome, *continuation, error) {
	out := Outcome{OperationID: op.ID, Before: op.State, After: ops.RecoveryRequired, Action: "recovery_required", Code: "recovery_required"}
	// Synchronous secret assignments are not detached deployment jobs and have
	// no plan. Never replay them through systemd or the deployment executor.
	if op.Kind == ops.SecretSet {
		if op.State == ops.Queued {
			out.After, out.Action, out.Code = op.State, "unchanged", ""
		}
		return out, nil, nil
	}
	id, err := systemd.ParseOperationID(op.ID)
	if err != nil {
		return out, nil, err
	}
	unit, err := systemd.ParseUnit("brine-op-" + id.String() + ".service")
	if err != nil {
		return out, nil, err
	}
	bound := r.ProbeTimeout
	if bound <= 0 {
		bound = 5 * time.Second
	}
	probe, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	properties, showErr := r.Systemd.Show(probe, unit)
	pending, pendingErr := r.Systemd.JobPending(probe, unit)
	var missing *localexec.Error
	absent := errors.As(showErr, &missing) && missing.Kind == localexec.NotFound
	if pendingErr == nil && pending || showErr == nil && (properties.ActiveState == "active" && properties.SubState == "running" || properties.ActiveState == "activating" || properties.ActiveState == "deactivating" || pendingErr == nil && pending) {
		out.After, out.Action, out.Code = op.State, "running", ""
		return out, nil, nil
	}
	dead := absent || showErr == nil && pendingErr == nil && !pending && (properties.ActiveState == "inactive" && properties.SubState == "dead" || properties.ActiveState == "failed" && properties.SubState == "failed" || properties.ActiveState == "active" && properties.SubState == "exited")
	if !dead {
		return out, nil, nil
	}
	events, err := r.events(ctx, op.ID)
	if err != nil {
		return out, nil, err
	}
	launch := false
	steps := false
	for _, event := range events {
		launch = launch || event.Kind == "launch"
		steps = steps || event.Kind == "step"
	}
	if op.State == ops.Queued && !launch && !steps {
		out.After, out.Action, out.Code = ops.Failed, "failed", "launch_failed"
		return out, nil, nil
	}
	if (op.State == ops.Queued || op.State == ops.LaunchUnknown) && !steps {
		// Collected or absent transient units cannot disprove an uncertain launch.
		if showErr == nil && properties.ActiveState == "failed" {
			out.After, out.Action, out.Code = ops.Failed, "failed", "executor_failed"
		}
		return out, nil, nil
	}
	// Interrupted recovery jobs are receipts, not deployment plans. Never replay
	// their recovery request or interpret them as a forward-effect prefix.
	if op.Kind == ops.Reconcile {
		return out, nil, nil
	}
	if r.Executor == nil && r.ExecutorFor == nil {
		return out, nil, nil
	}
	p, d, err := r.Store.LoadPlan(ctx, op.PlanID)
	if err != nil {
		return out, nil, nil
	}
	executor := r.Executor
	if r.ExecutorFor != nil {
		executor, err = r.ExecutorFor(ctx, op, p, d)
		if err != nil || executor == nil {
			return out, nil, nil
		}
	}
	recovery, err := executor.InspectRecovery(ctx, op, p, d, events)
	if err != nil {
		return out, nil, nil
	}
	out.Action, out.Step = string(recovery.Action), recovery.Step
	switch recovery.Action {
	case apply.ResumeForward:
		out.After, out.Code = ops.Succeeded, ""
	case apply.RestorePrevious:
		out.After, out.Code = ops.RolledBack, "stale_plan"
	case apply.FinishSucceeded:
		out.After, out.Code = ops.Succeeded, ""
	}
	return out, &continuation{executor: executor, assessment: recovery}, nil
}

func (r Reconciler) events(ctx context.Context, id string) ([]ops.Event, error) {
	events := []ops.Event{}
	var cursor uint64
	for {
		page, err := r.Store.EventsAfter(ctx, id, cursor, EventPageSize)
		if err != nil {
			return nil, err
		}
		for _, event := range page {
			if event.Sequence <= cursor || ops.ValidateEvent(event) != nil {
				return nil, ops.ErrInvalidEvent
			}
			cursor = event.Sequence
			events = append(events, event)
			if len(events) > MaxEvents {
				return nil, errors.New("reconcile: journal limit exceeded")
			}
		}
		if len(page) < EventPageSize {
			return events, nil
		}
	}
}
