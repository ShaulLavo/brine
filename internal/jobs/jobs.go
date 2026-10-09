// Package jobs records accepted intent and runs it independently of SSH observers.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/systemd"
)

const EventPageLimit = 128
const JournalTimeout = 5 * time.Second
const HostLockWaitTimeout = time.Minute
const LaunchLockWaitTimeout = 15 * time.Second

type Lock = ops.Lock
type RunnerStore interface {
	GetOperation(context.Context, string) (ops.Operation, error)
	LoadPlan(context.Context, string) (plan.Plan, policy.Desired, error)
	AcquireHostLock(context.Context) (ops.Lock, error)
	AppendEvent(context.Context, string, ops.Event) (uint64, error)
	SetOperationState(context.Context, string, ops.State) error
	TransitionOperation(context.Context, string, ops.State, ops.State) error
}
type Store interface {
	RunnerStore
	AcquireLaunchLock(context.Context) (ops.Lock, error)
	// The store atomically binds requester+key to one plan and one operation.
	CreateOperation(context.Context, ops.Intent, string, string) (ops.Operation, bool, error)
	EventsAfter(context.Context, string, uint64, int) ([]ops.Event, error)
}
type Launcher interface {
	Launch(context.Context, systemd.OperationID) error
}
type Executor interface {
	Run(context.Context, string, plan.Plan, policy.Desired) error
}

type Accepted struct {
	Status      string `json:"status"`
	OperationID string `json:"operation_id"`
}
type Status struct {
	Operation  ops.Operation `json:"operation"`
	Events     []ops.Event   `json:"events"`
	NextCursor uint64        `json:"next_cursor"`
}
type Service struct {
	Store     Store
	Launcher  Launcher
	Requester string
}

func (s Service) Apply(ctx context.Context, planID, key string) (accepted Accepted, err error) {
	if s.Store == nil || s.Launcher == nil || s.Requester == "" {
		return Accepted{}, result.New(result.DependencyMissing, nil)
	}
	if !ValidPlanID(planID) || !ValidID(key) {
		return Accepted{}, result.New(result.DispatchInvalidRequest, nil)
	}
	// The launcher holds only the short launch fence, never the deploy lock.
	// Acquiring this fence after process death proves no creator/launcher remains.
	wait, stop := context.WithTimeout(ctx, LaunchLockWaitTimeout)
	lock, err := s.Store.AcquireLaunchLock(wait)
	stop()
	if err != nil {
		return Accepted{}, err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	op, existing, err := s.Store.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: planID}, s.Requester, key)
	if err != nil {
		return Accepted{}, err
	}
	return s.launch(ctx, op, existing)
}

func (s Service) launch(ctx context.Context, op ops.Operation, existing bool) (accepted Accepted, err error) {
	id, err := systemd.ParseOperationID(op.ID)
	if err != nil {
		return Accepted{}, result.New(result.InternalError, err)
	}
	accepted = Accepted{Status: "accepted", OperationID: op.ID}
	if existing {
		return accepted, nil
	}
	// After the durable record exists, a departing observer cannot cancel launch.
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if _, err = s.Store.AppendEvent(work, op.ID, launchEvent("intent")); err != nil {
		return Accepted{}, err
	}
	launchErr := s.Launcher.Launch(work, id)
	if launchErr == nil {
		_, err = s.Store.AppendEvent(work, op.ID, launchEvent("completed"))
		return accepted, err
	}
	state, code := ops.Failed, "launch_failed"
	var runtime *localexec.Error
	unknown := errors.As(launchErr, &runtime) && (runtime.Kind == localexec.UnknownOutcome || runtime.Kind == localexec.Timeout)
	if unknown {
		state, code = ops.LaunchUnknown, "launch_unknown"
	}
	journal, stop := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
	defer stop()
	if unknown {
		if _, err = s.Store.AppendEvent(journal, op.ID, launchEvent("unknown")); err != nil {
			return Accepted{}, err
		}
	}
	if _, err = s.Store.AppendEvent(journal, op.ID, failureEvent(code)); err != nil {
		return Accepted{}, err
	}
	if err = s.Store.TransitionOperation(journal, op.ID, ops.Queued, state); err != nil {
		if errors.Is(err, ops.ErrStateConflict) {
			current, readErr := s.Store.GetOperation(journal, op.ID)
			if readErr == nil && current.State != ops.Queued {
				return accepted, nil
			}
		}
		return Accepted{}, err
	}
	if unknown {
		return accepted, nil
	}
	return Accepted{}, result.New(result.InternalError, launchErr)
}

func (s Service) Operation(ctx context.Context, id string, cursor uint64) (Status, error) {
	if s.Store == nil {
		return Status{}, result.New(result.DependencyMissing, nil)
	}
	if !ValidID(id) {
		return Status{}, result.New(result.DispatchInvalidRequest, nil)
	}
	op, err := s.Store.GetOperation(ctx, id)
	if err != nil {
		return Status{}, err
	}
	events, err := s.Store.EventsAfter(ctx, id, cursor, EventPageLimit)
	if err != nil {
		return Status{}, err
	}
	if events == nil {
		events = []ops.Event{}
	}
	next := cursor
	for _, event := range events {
		next = event.Sequence
	}
	return Status{Operation: op, Events: events, NextCursor: next}, nil
}

var planIDPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func ValidPlanID(raw string) bool { return planIDPattern.MatchString(raw) }

func ValidID(raw string) bool { _, err := systemd.ParseOperationID(raw); return err == nil }
func launchEvent(outcome string) ops.Event {
	data, _ := json.Marshal(ops.LaunchPayload{Outcome: outcome})
	return ops.Event{Kind: "launch", Payload: data}
}
func failureEvent(code string) ops.Event {
	data, _ := json.Marshal(ops.FailurePayload{Code: code})
	return ops.Event{Kind: "failure", Payload: data}
}

type Reconciler interface {
	ReconcileUnderLock(context.Context, ops.Lock, string, bool) error
}

type Runner struct {
	Reconciler      Reconciler
	Recovery        func(context.Context, string) error
	Store           RunnerStore
	Executor        Executor
	LockWaitTimeout time.Duration // Zero uses HostLockWaitTimeout; not request-controlled.
}

func (r Runner) Run(ctx context.Context, id string) (err error) {
	if !ValidID(id) {
		return result.New(result.InvalidUsage, nil)
	}
	if r.Store == nil {
		return result.New(result.DependencyMissing, nil)
	}
	op, err := r.Store.GetOperation(ctx, id)
	if err != nil {
		return err
	}
	if op.Kind == ops.Reconcile {
		return r.runReconcile(ctx, op)
	}
	if op.Kind != ops.Deploy && op.Kind != ops.Resolve {
		return result.New(result.Conflict, nil)
	}
	if r.Executor == nil {
		return result.New(result.DependencyMissing, nil)
	}
	bound := r.LockWaitTimeout
	if bound <= 0 {
		bound = HostLockWaitTimeout
	}
	wait, stop := context.WithTimeout(ctx, bound)
	lock, err := r.Store.AcquireHostLock(wait)
	stop()
	if err != nil {
		return r.lockUnavailable(ctx, id, err)
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	if r.Reconciler != nil {
		if err := r.Reconciler.ReconcileUnderLock(ctx, lock, id, false); err != nil {
			return err
		}
	}
	op, err = r.Store.GetOperation(ctx, id)
	if err != nil {
		return err
	}
	if op.Kind != ops.Deploy && op.Kind != ops.Resolve || op.State != ops.Queued && op.State != ops.LaunchUnknown {
		return result.New(result.Conflict, nil)
	}
	var runErr error
	if op.Kind == ops.Resolve {
		resolver, ok := r.Reconciler.(interface {
			RunResolution(context.Context, ops.Lock, string) error
		})
		if !ok {
			runErr = result.New(result.DependencyMissing, nil)
		} else {
			runErr = resolver.RunResolution(ctx, lock, id)
		}
	} else {
		intent, desired, loadErr := r.Store.LoadPlan(ctx, op.PlanID)
		if loadErr != nil {
			runErr = loadErr
		} else {
			runErr = r.Executor.Run(ctx, id, intent, desired)
		}
	}

	journal, cancel := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
	defer cancel()
	current, err := r.Store.GetOperation(journal, id)
	if err != nil {
		return errors.Join(runErr, err)
	}
	if current.State.IsTerminal() {
		return runErr
	}
	if runErr != nil && result.Classify(runErr).Code() == result.Conflict && (current.State == ops.Queued || current.State == ops.LaunchUnknown) {
		_, eventErr := r.Store.AppendEvent(journal, id, failureEvent("stale_plan"))
		stateErr := r.Store.TransitionOperation(journal, id, current.State, ops.Failed)
		return errors.Join(runErr, eventErr, stateErr)
	}
	code := "executor_incomplete"
	if runErr != nil {
		code = "executor_failed"
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		code = "interrupted"
	}
	state := ops.RecoveryRequired
	if op.Kind != ops.Resolve && (current.State == ops.Queued || current.State == ops.LaunchUnknown || current.State == ops.Preflight) {
		state = ops.Failed
	}
	return r.fail(journal, id, code, state, runErr)
}
func (r Runner) lockUnavailable(ctx context.Context, id string, cause error) error {
	journal, cancel := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
	defer cancel()
	op, err := r.Store.GetOperation(journal, id)
	if err != nil {
		return err
	}
	if op.Kind != ops.Deploy && op.Kind != ops.Resolve {
		return result.New(result.Conflict, nil)
	}
	_, eventErr := r.Store.AppendEvent(journal, id, failureEvent("lock_unavailable"))
	stateErr := r.Store.TransitionOperation(journal, id, ops.Queued, ops.Failed)
	if errors.Is(stateErr, ops.ErrStateConflict) {
		current, readErr := r.Store.GetOperation(journal, id)
		if readErr != nil {
			stateErr = errors.Join(stateErr, readErr)
		} else if current.State != ops.Queued {
			// A different runner advanced this operation; never replace its state.
			stateErr = nil
		}
	}
	return errors.Join(result.New(result.InternalError, cause), eventErr, stateErr)
}

func (r Runner) fail(ctx context.Context, id, code string, state ops.State, cause error) error {
	journal, cancel := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
	defer cancel()
	_, eventErr := r.Store.AppendEvent(journal, id, failureEvent(code))
	stateErr := r.Store.SetOperationState(journal, id, state)
	return errors.Join(result.New(result.RecoveryRequired, cause), eventErr, stateErr)
}

// Reconcile accepts intent under the same short launch fence as apply. Actual
// recovery runs in the existing detached, bounded run-op transient unit.
func (s Service) Reconcile(ctx context.Context) (accepted Accepted, err error) {
	state, ok := s.Store.(interface {
		CreateReconcileOperation(context.Context, string) (ops.Operation, error)
	})
	if !ok || s.Launcher == nil || s.Requester == "" {
		return Accepted{}, result.New(result.DependencyMissing, nil)
	}
	wait, cancel := context.WithTimeout(ctx, LaunchLockWaitTimeout)
	lock, err := s.Store.AcquireLaunchLock(wait)
	cancel()
	if err != nil {
		return Accepted{}, err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	op, err := state.CreateReconcileOperation(ctx, s.Requester)
	if err != nil {
		return Accepted{}, err
	}
	return s.launch(ctx, op, false)
}

func (r Runner) runReconcile(ctx context.Context, op ops.Operation) error {
	if r.Recovery == nil {
		return result.New(result.DependencyMissing, nil)
	}
	if op.State != ops.Queued && op.State != ops.LaunchUnknown {
		return result.New(result.Conflict, nil)
	}
	// CAS is the single-run gate. Do not acquire host before the recovery engine's
	// launch fence: that would reverse the established lock order.
	if err := r.Store.TransitionOperation(ctx, op.ID, op.State, ops.Preflight); err != nil {
		return err
	}
	runErr := r.Recovery(ctx, op.ID)
	journal, cancel := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
	defer cancel()
	to := ops.Succeeded
	var eventErr error
	if runErr != nil {
		to = ops.RecoveryRequired
		code := "executor_failed"
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			code = "interrupted"
		}
		if result.Classify(runErr).Code() == result.RecoveryRequired {
			code = "recovery_required"
		}
		if errors.Is(runErr, ops.ErrLockUnavailable) {
			to, code = ops.Failed, "lock_unavailable"
		}
		_, eventErr = r.Store.AppendEvent(journal, op.ID, failureEvent(code))
	}
	stateErr := r.Store.TransitionOperation(journal, op.ID, ops.Preflight, to)
	return errors.Join(runErr, eventErr, stateErr)
}
