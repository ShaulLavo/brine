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

type Lock = ops.Lock
type RunnerStore interface {
	GetOperation(context.Context, string) (ops.Operation, error)
	LoadPlan(context.Context, string) (plan.Plan, policy.Desired, error)
	AcquireHostLock(context.Context) (ops.Lock, error)
	AppendEvent(context.Context, string, ops.Event) (uint64, error)
	SetOperationState(context.Context, string, ops.State) error
}
type Store interface {
	RunnerStore
	// The store atomically binds requester+key to one plan and one operation.
	CreateOperation(context.Context, string, string, string) (ops.Operation, bool, error)
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

func (s Service) Apply(ctx context.Context, planID, key string) (Accepted, error) {
	if s.Store == nil || s.Launcher == nil || s.Requester == "" {
		return Accepted{}, result.New(result.DependencyMissing, nil)
	}
	if !ValidPlanID(planID) || !ValidID(key) {
		return Accepted{}, result.New(result.DispatchInvalidRequest, nil)
	}
	op, existing, err := s.Store.CreateOperation(ctx, planID, s.Requester, key)
	if err != nil {
		return Accepted{}, err
	}
	id, err := systemd.ParseOperationID(op.ID)
	if err != nil {
		return Accepted{}, result.New(result.InternalError, err)
	}
	accepted := Accepted{Status: "accepted", OperationID: op.ID}
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
	current, err := s.Store.GetOperation(journal, op.ID)
	if err != nil {
		return Accepted{}, err
	}
	if current.State != ops.Queued {
		return accepted, nil
	}
	if err = s.Store.SetOperationState(journal, op.ID, state); err != nil {
		// The job may have progressed between inspection and this transition.
		current, readErr := s.Store.GetOperation(journal, op.ID)
		if readErr == nil && current.State != ops.Queued {
			return accepted, nil
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

type Runner struct {
	Store    RunnerStore
	Executor Executor
}

func (r Runner) Run(ctx context.Context, id string) (err error) {
	if !ValidID(id) {
		return result.New(result.InvalidUsage, nil)
	}
	if r.Store == nil || r.Executor == nil {
		return result.New(result.DependencyMissing, nil)
	}
	lock, err := r.Store.AcquireHostLock(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	op, err := r.Store.GetOperation(ctx, id)
	if err != nil {
		return err
	}
	if op.State != ops.Queued && op.State != ops.LaunchUnknown {
		return result.New(result.Conflict, nil)
	}
	intent, desired, err := r.Store.LoadPlan(ctx, op.PlanID)
	if err != nil {
		return r.fail(ctx, id, "executor_failed", ops.Failed, err)
	}
	runErr := r.Executor.Run(ctx, id, intent, desired)
	journal, cancel := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
	defer cancel()
	current, err := r.Store.GetOperation(journal, id)
	if err != nil {
		return errors.Join(runErr, err)
	}
	if current.State.IsTerminal() {
		return runErr
	}
	code := "executor_incomplete"
	if runErr != nil {
		code = "executor_failed"
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		code = "interrupted"
	}
	state := ops.RecoveryRequired
	if current.State == ops.Queued || current.State == ops.LaunchUnknown || current.State == ops.Preflight {
		state = ops.Failed
	}
	return r.fail(journal, id, code, state, runErr)
}
func (r Runner) fail(ctx context.Context, id, code string, state ops.State, cause error) error {
	journal, cancel := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
	defer cancel()
	_, eventErr := r.Store.AppendEvent(journal, id, failureEvent(code))
	stateErr := r.Store.SetOperationState(journal, id, state)
	return errors.Join(result.New(result.RecoveryRequired, cause), eventErr, stateErr)
}
