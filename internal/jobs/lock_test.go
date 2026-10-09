package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

type lockErrorStore struct {
	*fakeStore
	acquire func(context.Context) (Lock, error)
	reads   int
}

func (s *lockErrorStore) AcquireHostLock(ctx context.Context) (Lock, error) {
	return s.acquire(ctx)
}
func (s *lockErrorStore) AppendEvent(ctx context.Context, id string, event ops.Event) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return s.fakeStore.AppendEvent(ctx, id, event)
}
func (s *lockErrorStore) TransitionOperation(ctx context.Context, id string, from, to ops.State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.fakeStore.TransitionOperation(ctx, id, from, to)
}
func (s *lockErrorStore) GetOperation(ctx context.Context, id string) (ops.Operation, error) {
	if err := ctx.Err(); err != nil {
		return ops.Operation{Kind: ops.Deploy}, err
	}
	s.reads++
	return s.fakeStore.GetOperation(ctx, id)
}

func TestRunnerLockErrorsJournalOutsideCanceledContext(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		t.Run(map[bool]string{false: "acquisition_error", true: "parent_canceled"}[cancelParent], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := errors.New("fixture acquisition failed")
			s := &lockErrorStore{fakeStore: &fakeStore{operation: ops.Operation{Kind: ops.Deploy, ID: "op1", State: ops.Queued}}}
			s.acquire = func(wait context.Context) (Lock, error) {
				deadline, ok := wait.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > HostLockWaitTimeout {
					t.Fatal("lock wait has no default bound")
				}
				if cancelParent {
					cancel()
					cause = context.Canceled
				}
				return nil, cause
			}
			runner := Runner{Store: s, Executor: execFunc(func(context.Context, string, plan.Plan, policy.Desired) error {
				t.Fatal("executor ran without the lock")
				return nil
			})}
			if err := runner.Run(ctx, "op1"); !errors.Is(err, cause) {
				t.Fatalf("acquisition error lost: %v", err)
			}
			if s.operation.State != ops.Failed || len(s.events) != 1 {
				t.Fatalf("operation stranded: state=%s events=%+v", s.operation.State, s.events)
			}
			var failure ops.FailurePayload
			if err := json.Unmarshal(s.events[0].Payload, &failure); err != nil || failure.Code != "lock_unavailable" || s.events[0].Kind != "failure" || s.events[0].State != "" {
				t.Fatalf("unsafe or missing lock failure: %+v", s.events[0])
			}
		})
	}
}

func TestRunnerLockFailureCannotOverwriteAnotherRunner(t *testing.T) {
	for _, state := range []ops.State{ops.Preflight, ops.Preparing, ops.Succeeded, ops.Failed, ops.LaunchUnknown} {
		t.Run(string(state), func(t *testing.T) {
			cause := errors.New("fixture acquisition failed")
			base := &fakeStore{operation: ops.Operation{Kind: ops.Deploy, ID: "op1", State: ops.Queued}}
			base.beforeTransition = func() { base.operation.State = state }
			s := &lockErrorStore{fakeStore: base, acquire: func(context.Context) (Lock, error) { return nil, cause }}
			runner := Runner{Store: s, Executor: execFunc(func(context.Context, string, plan.Plan, policy.Desired) error {
				t.Fatal("duplicate executor ran")
				return nil
			})}
			if err := runner.Run(context.Background(), "op1"); !errors.Is(err, cause) {
				t.Fatalf("acquisition error lost: %v", err)
			}
			if base.operation.State != state || s.reads != 3 {
				t.Fatalf("conflict not reconciled: state=%s reads=%d", base.operation.State, s.reads)
			}
		})
	}
}

func TestRunnerWaitsForLockBeforeExecution(t *testing.T) {
	waiting := make(chan struct{})
	release := make(chan struct{})
	executed := make(chan struct{})
	base := &fakeStore{operation: ops.Operation{Kind: ops.Deploy, ID: "op1", State: ops.Queued}}
	s := &lockErrorStore{fakeStore: base}
	s.acquire = func(ctx context.Context) (Lock, error) {
		close(waiting)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return base.AcquireHostLock(ctx)
		}
	}
	runner := Runner{Store: s, LockWaitTimeout: time.Second, Executor: execFunc(func(context.Context, string, plan.Plan, policy.Desired) error {
		close(executed)
		base.operation.State = ops.Succeeded
		return nil
	})}
	done := make(chan error, 1)
	go func() { done <- runner.Run(context.Background(), "op1") }()
	<-waiting
	select {
	case <-executed:
		t.Error("executor started before lock acquisition")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-executed:
	default:
		t.Fatal("executor did not start after lock acquisition")
	}
}
