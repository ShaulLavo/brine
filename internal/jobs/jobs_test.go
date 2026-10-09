package jobs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type fakeStore struct {
	mu               sync.Mutex
	operation        ops.Operation
	events           []ops.Event
	creates          int
	locked           bool
	desired          policy.Desired
	loadErr          error
	beforeTransition func()
}

func (s *fakeStore) CreateOperation(_ context.Context, planID, requester, key string) (ops.Operation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.operation.ID != "" {
		if s.operation.PlanID != planID || s.operation.Requester != requester || s.operation.IdempotencyKey != key {
			return ops.Operation{}, false, errors.New("conflict")
		}
		return s.operation, true, nil
	}
	s.creates++
	s.operation = ops.Operation{ID: "op1", PlanID: planID, Requester: requester, IdempotencyKey: key, State: ops.Queued}
	return s.operation, false, nil
}
func (s *fakeStore) GetOperation(_ context.Context, id string) (ops.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.operation.ID {
		return ops.Operation{}, errors.New("missing")
	}
	return s.operation, nil
}
func (s *fakeStore) LoadPlan(_ context.Context, id string) (plan.Plan, policy.Desired, error) {
	if !s.locked {
		return plan.Plan{}, policy.Desired{}, errors.New("load without lock")
	}
	return plan.Plan{Hash: id}, s.desired, s.loadErr
}
func (s *fakeStore) AcquireHostLock(context.Context) (Lock, error) {
	if s.locked {
		return nil, errors.New("busy")
	}
	s.locked = true
	return fakeLock{s}, nil
}

type fakeLock struct{ s *fakeStore }

func (l fakeLock) Release() error { l.s.locked = false; return nil }
func (s *fakeStore) AppendEvent(_ context.Context, _ string, e ops.Event) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Kind == "state" {
		return 0, ops.ErrInvalidEvent
	}
	if err := ops.ValidateEvent(e); err != nil {
		return 0, err
	}
	e.Sequence = uint64(len(s.events) + 1)
	s.events = append(s.events, e)
	return e.Sequence, nil
}
func (s *fakeStore) TransitionOperation(_ context.Context, _ string, from, to ops.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.beforeTransition != nil {
		s.beforeTransition()
	}
	if s.operation.State != from {
		return &ops.StateConflictError{Current: s.operation.State}
	}
	if !ops.CanTransition(from, to) {
		return errors.New("transition")
	}
	s.operation.State = to
	return nil
}

func (s *fakeStore) SetOperationState(_ context.Context, _ string, state ops.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ops.CanTransition(s.operation.State, state) {
		return errors.New("transition")
	}
	s.operation.State = state
	return nil
}
func (s *fakeStore) EventsAfter(_ context.Context, _ string, cursor uint64, limit int) ([]ops.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ops.Event{}
	for _, e := range s.events {
		if e.Sequence > cursor && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

type launchFunc func(context.Context, systemd.OperationID) error

func (f launchFunc) Launch(c context.Context, id systemd.OperationID) error { return f(c, id) }

type execFunc func(context.Context, string, plan.Plan, policy.Desired) error

func (f execFunc) Run(c context.Context, id string, p plan.Plan, d policy.Desired) error {
	return f(c, id, p, d)
}

func TestApplyRecordsBeforeLaunchAndDeduplicates(t *testing.T) {
	store := &fakeStore{}
	count := 0
	service := Service{Store: store, Requester: "runner", Launcher: launchFunc(func(ctx context.Context, id systemd.OperationID) error {
		count++
		if store.operation.ID != id.String() || len(store.events) != 1 || store.events[0].Kind != "launch" {
			t.Fatal("unit before durable intent")
		}
		return nil
	})}
	first, err := service.Apply(context.Background(), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "key1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Apply(context.Background(), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "key1")
	if err != nil {
		t.Fatal(err)
	}
	if first.OperationID != second.OperationID || first.Status != "accepted" || store.creates != 1 || count != 1 {
		t.Fatalf("first=%+v second=%+v creates=%d launches=%d", first, second, store.creates, count)
	}
}

func TestApplyLaunchOutcomes(t *testing.T) {
	for _, tc := range []struct {
		kind    localexec.ErrorKind
		state   ops.State
		wantErr bool
	}{
		{localexec.Failed, ops.Failed, true}, {localexec.UnknownOutcome, ops.LaunchUnknown, false},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			s := &fakeStore{}
			service := Service{Store: s, Requester: "runner", Launcher: launchFunc(func(context.Context, systemd.OperationID) error { return &localexec.Error{Kind: tc.kind} })}
			accepted, err := service.Apply(context.Background(), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "key1")
			if (err != nil) != tc.wantErr || s.operation.State != tc.state {
				t.Fatalf("state=%s err=%v", s.operation.State, err)
			}
			if !tc.wantErr && accepted.OperationID != "op1" {
				t.Fatal("lost operation ID")
			}
			again, err := service.Apply(context.Background(), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "key1")
			if err != nil || again.OperationID != "op1" {
				t.Fatal("cannot recover recorded ID", err)
			}
		})
	}
}

func TestConcurrentIdempotentApply(t *testing.T) {
	s := &fakeStore{}
	var mu sync.Mutex
	launches := 0
	service := Service{Store: s, Requester: "runner", Launcher: launchFunc(func(context.Context, systemd.OperationID) error { mu.Lock(); defer mu.Unlock(); launches++; return nil })}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := service.Apply(context.Background(), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "key1"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if launches != 1 {
		t.Fatalf("launched %d times", launches)
	}
}

func TestRunnerOwnsLockAndLoadsExactDesired(t *testing.T) {
	s := &fakeStore{operation: ops.Operation{ID: "op1", PlanID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: ops.Queued}, desired: policy.Desired{PolicyVersion: "retained"}}
	ran := false
	runner := Runner{Store: s, Executor: execFunc(func(ctx context.Context, id string, p plan.Plan, d policy.Desired) error {
		ran = true
		if !s.locked || id != "op1" || p.Hash != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || d.PolicyVersion != "retained" {
			t.Fatal("executor input or lock")
		}
		s.operation.State = ops.Succeeded
		return nil
	})}
	if err := runner.Run(context.Background(), "op1"); err != nil {
		t.Fatal(err)
	}
	if !ran || s.locked || s.operation.State != ops.Succeeded {
		t.Fatal("runner did not complete")
	}
	if err := runner.Run(context.Background(), "op1"); err == nil {
		t.Fatal("terminal operation reran")
	}
}

func TestRunnerNeverInventsSuccess(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s := &fakeStore{operation: ops.Operation{ID: "op1", PlanID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: ops.Queued}}
		runner := Runner{Store: s, Executor: execFunc(func(context.Context, string, plan.Plan, policy.Desired) error {
			s.operation.State = ops.Preparing
			if fail {
				return errors.New("secret error")
			}
			return nil
		})}
		if err := runner.Run(context.Background(), "op1"); err == nil || s.operation.State != ops.RecoveryRequired || s.locked {
			t.Fatalf("state=%s err=%v", s.operation.State, err)
		}
		if len(s.events) != 1 || s.events[0].Kind != "failure" {
			t.Fatal("missing safe failure event")
		}
	}
}

func TestOperationCursor(t *testing.T) {
	s := &fakeStore{operation: ops.Operation{ID: "op1", PlanID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: ops.Succeeded}, events: []ops.Event{{Sequence: 1, Kind: "state", State: ops.Preflight}, {Sequence: 2, Kind: "state", State: ops.Succeeded}}}
	got, err := (Service{Store: s}).Operation(context.Background(), "op1", 1)
	if err != nil || got.NextCursor != 2 || len(got.Events) != 1 || got.Events[0].Sequence != 2 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestApplyFinishesLaunchAfterObserverCancellation(t *testing.T) {
	s := &fakeStore{}
	ctx, cancel := context.WithCancel(context.Background())
	service := Service{Store: s, Requester: "runner", Launcher: launchFunc(func(ctx context.Context, _ systemd.OperationID) error {
		cancel()
		if ctx.Err() != nil {
			t.Fatal("launch bound to observer cancellation")
		}
		return nil
	})}
	accepted, err := service.Apply(ctx, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "key1")
	if err != nil || accepted.OperationID != "op1" || len(s.events) != 2 {
		t.Fatalf("%+v %v", accepted, err)
	}
}

func TestCanonicalStoredPlanID(t *testing.T) {
	planID := "sha256:" + strings.Repeat("a", 64)
	s := &fakeStore{}
	service := Service{Store: s, Requester: "runner", Launcher: launchFunc(func(context.Context, systemd.OperationID) error { return nil })}
	if _, err := service.Apply(context.Background(), planID, "key1"); err != nil {
		t.Fatalf("stored plan hash refused: %v", err)
	}
}

func TestLaunchOutcomeCannotOverwriteExecutorProgress(t *testing.T) {
	for _, kind := range []localexec.ErrorKind{localexec.Failed, localexec.UnknownOutcome} {
		s := &fakeStore{}
		s.beforeTransition = func() { s.operation.State = ops.Preflight }
		service := Service{Store: s, Requester: "runner", Launcher: launchFunc(func(context.Context, systemd.OperationID) error { return &localexec.Error{Kind: kind} })}
		accepted, err := service.Apply(context.Background(), "sha256:"+strings.Repeat("a", 64), "key1")
		if err != nil || accepted.OperationID != "op1" || s.operation.State != ops.Preflight {
			t.Fatalf("clobbered progress: %+v state=%s err=%v", accepted, s.operation.State, err)
		}
	}
}
