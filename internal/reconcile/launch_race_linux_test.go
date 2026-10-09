//go:build linux

package reconcile

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type pausedCreationStore struct {
	*store.Store
	created chan ops.Operation
	resume  <-chan struct{}
	pause   bool
}

func (s pausedCreationStore) CreateOperation(ctx context.Context, intent ops.Intent, requester, key string) (ops.Operation, bool, error) {
	op, existing, err := s.Store.CreateOperation(ctx, intent, requester, key)
	if err == nil && !existing {
		s.created <- op
		if s.pause {
			select {
			case <-s.resume:
			case <-ctx.Done():
				return op, false, ctx.Err()
			}
		}
	}
	return op, existing, err
}

type slowLauncher func(context.Context, systemd.OperationID) error

func (f slowLauncher) Launch(ctx context.Context, id systemd.OperationID) error { return f(ctx, id) }

func TestReconcileCannotTerminalizeSlowLauncher(t *testing.T) {
	for _, phase := range []string{"before_intent", "inside_launch"} {
		t.Run(phase, func(t *testing.T) {
			s, fixtureOp, _ := fixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resume := make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(resume) }) }
			defer release()
			created := make(chan ops.Operation, 1)
			launching := make(chan struct{})
			var unitExists atomic.Bool
			service := jobs.Service{Store: pausedCreationStore{Store: s, created: created, resume: resume, pause: phase == "before_intent"}, Requester: "fixture-requester", Launcher: slowLauncher(func(ctx context.Context, _ systemd.OperationID) error {
				close(launching)
				if phase == "inside_launch" {
					select {
					case <-resume:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				unitExists.Store(true)
				return nil
			})}
			done := make(chan error, 1)
			go func() { _, err := service.Apply(ctx, fixtureOp.PlanID, "slow-launch-key"); done <- err }()
			var op ops.Operation
			select {
			case op = <-created:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if phase == "inside_launch" {
				select {
				case <-launching:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			fake := absentRunner()
			fake.ShowFunc = func(_ context.Context, unit systemd.Unit) (systemd.Properties, error) {
				if unit.String() == "brine-op-"+op.ID+".service" && unitExists.Load() {
					return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
				}
				return systemd.Properties{}, &localexec.Error{Kind: localexec.NotFound}
			}
			reconciler := Reconciler{Store: s, Systemd: fake, LockTimeout: 20 * time.Millisecond}
			if _, err := reconciler.DryRun(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("preview entered active launcher window: %v", err)
			}
			if _, err := reconciler.Reconcile(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("reconcile entered active launcher window: %v", err)
			}
			current, err := s.GetOperation(ctx, op.ID)
			if err != nil || current.State != ops.Queued {
				t.Fatalf("state %s error %v", current.State, err)
			}
			release()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if _, err := reconciler.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			current, err = s.GetOperation(ctx, op.ID)
			if err != nil || current.State != ops.Queued {
				t.Fatalf("accepted runner was terminalized: state %s error %v", current.State, err)
			}
		})
	}
}

func TestApplyLaunchDoesNotWaitForRunningDeploy(t *testing.T) {
	s, op, _ := fixture(t)
	lock, err := s.AcquireHostLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	// The lock stays held for the entire test, so taking it would still fail
	// at this deadline. Allow durable journal I/O under full race-suite load.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	launched := false
	service := jobs.Service{Store: s, Requester: "fixture-requester", Launcher: slowLauncher(func(context.Context, systemd.OperationID) error { launched = true; return nil })}
	accepted, err := service.Apply(ctx, op.PlanID, "next-deploy-key")
	if err != nil || !launched || accepted.OperationID == "" {
		t.Fatalf("apply waited for the deploy lock: accepted %+v error %v", accepted, err)
	}
	// Startup reconciliation already holds the host lock and must not take the
	// launch fence in reverse order, even when a launcher owns it concurrently.
	launch, err := s.AcquireLaunchLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Release()
	reconciler := Reconciler{Store: s, Systemd: absentRunner()}
	report, err := reconciler.ReconcileUnderLock(ctx, lock, op.ID, false)
	if err != nil || len(report.Outcomes) != 1 || report.Outcomes[0].Action != "unchanged" {
		t.Fatalf("report %+v error %v", report, err)
	}
	current, err := s.GetOperation(ctx, accepted.OperationID)
	if err != nil || current.State != ops.Queued {
		t.Fatalf("state %s error %v", current.State, err)
	}
	if err := s.TransitionOperation(ctx, accepted.OperationID, ops.Queued, ops.LaunchUnknown); err != nil {
		t.Fatal(err)
	}
	report, err = reconciler.ReconcileUnderLock(ctx, lock, op.ID, false)
	if err != nil || len(report.Outcomes) != 1 || report.Outcomes[0].Action != "unchanged" || report.Outcomes[0].After != ops.LaunchUnknown {
		t.Fatalf("launch-unknown report %+v error %v", report, err)
	}
}

func TestReconcileReleasesLaunchFenceBeforeDeployRecovery(t *testing.T) {
	s, op, _ := fixture(t)
	if err := s.TransitionOperation(context.Background(), op.ID, ops.Queued, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	fake := absentRunner()
	inspected := false
	fake.ShowFunc = func(ctx context.Context, _ systemd.Unit) (systemd.Properties, error) {
		wait, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		lock, err := s.AcquireLaunchLock(wait)
		if err != nil {
			t.Fatalf("launch fence retained during deployment inspection: %v", err)
		}
		lock.Release()
		inspected = true
		return systemd.Properties{}, &localexec.Error{Kind: localexec.NotFound}
	}
	if _, err := (Reconciler{Store: s, Systemd: fake}).Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !inspected {
		t.Fatal("deployment not inspected")
	}
}

type observedLaunchStore struct {
	*store.Store
	acquired chan struct{}
	once     sync.Once
}

func (s *observedLaunchStore) AcquireLaunchLock(ctx context.Context) (ops.Lock, error) {
	lock, err := s.Store.AcquireLaunchLock(ctx)
	if err == nil {
		s.once.Do(func() { close(s.acquired) })
	}
	return lock, err
}

func TestWaitingReconcileDoesNotDelayApplyAcceptance(t *testing.T) {
	s, op, _ := fixture(t)
	host, err := s.AcquireHostLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	observed := &observedLaunchStore{Store: s, acquired: make(chan struct{})}
	done := make(chan error, 1)
	finished := make(chan struct{})
	defer func() { cancel(); <-finished }()
	go func() {
		defer close(finished)
		_, err := (Reconciler{Store: observed, Systemd: absentRunner(), LockTimeout: 20 * time.Second}).Reconcile(ctx)
		done <- err
	}()
	select {
	case <-observed.acquired:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	launched := false
	service := jobs.Service{Store: s, Requester: "fixture-requester", Launcher: slowLauncher(func(context.Context, systemd.OperationID) error {
		launched = true
		return nil
	})}
	start := time.Now()
	accepted, err := service.Apply(ctx, op.PlanID, "waiting-recovery-key")
	if err != nil || !launched || accepted.OperationID == "" || time.Since(start) >= jobs.LaunchLockWaitTimeout {
		t.Fatalf("waiting recovery blocked apply acceptance: accepted %+v elapsed %s error %v", accepted, time.Since(start), err)
	}
	select {
	case err := <-done:
		t.Fatalf("recovery did not wait for the running deploy: %v", err)
	default:
	}
	if err := host.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
