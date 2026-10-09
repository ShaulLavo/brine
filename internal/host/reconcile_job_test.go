package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type observerBoundRecovery struct{ calls int }

func (r *observerBoundRecovery) Reconcile(ctx context.Context) (reconcile.Report, error) {
	r.calls++
	<-ctx.Done()
	return reconcile.Report{}, ctx.Err()
}
func (*observerBoundRecovery) DryRun(context.Context) (reconcile.Report, error) {
	return reconcile.Report{DryRun: true, Outcomes: []reconcile.Outcome{}}, nil
}

func TestRemoteReconcileAcceptsBeforeObserverDeadline(t *testing.T) {
	r := newDeployRig(t)
	inline := &observerBoundRecovery{}
	r.server.Reconciler = inline
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	args, _ := json.Marshal(dispatch.ReconcileArgs{DryRun: false})
	request, _ := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: 1, Op: "reconcile", RequestID: "recovery", Args: args})
	response, err := r.server.Handle(ctx, bytes.NewReader(request))
	if err != nil || !response.OK {
		t.Fatalf("recovery inherited observer deadline instead of accepting a detached job: %v", err)
	}
	accepted, ok := response.Data.(jobs.Accepted)
	if !ok || accepted.Status != "accepted" || !jobs.ValidID(accepted.OperationID) || inline.calls != 0 {
		t.Fatalf("inline recovery or invalid acceptance: %#v calls=%d", response.Data, inline.calls)
	}
	if len(r.launcher.ids) != 1 || r.launcher.ids[0] != accepted.OperationID {
		t.Fatal("recovery intent was not launched")
	}
	if _, err := r.store.GetOperation(context.Background(), accepted.OperationID); err != nil {
		t.Fatal("accepted job has no durable intent")
	}
}

func TestDetachedRecoveryOutlivesObserverAndIsPollable(t *testing.T) {
	r := newDeployRig(t)
	observer, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	args, _ := json.Marshal(dispatch.ReconcileArgs{DryRun: false})
	request, _ := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: 1, Op: "reconcile", RequestID: "recovery", Args: args})
	response, err := r.server.Handle(observer, bytes.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	accepted := response.Data.(jobs.Accepted)
	started, release := make(chan struct{}), make(chan struct{})
	recovery := r.runner.Recovery
	r.runner.Recovery = func(ctx context.Context, id string) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 15*time.Second {
			return context.DeadlineExceeded
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return recovery(ctx, id)
	}
	worker, cancelWorker := context.WithTimeout(context.Background(), time.Minute)
	defer cancelWorker()
	done := make(chan error, 1)
	go func() { done <- r.runner.Run(worker, accepted.OperationID) }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("worker inherited observer deadline: %v", err)
	case <-worker.Done():
		t.Fatal(worker.Err())
	}
	<-observer.Done()
	select {
	case err := <-done:
		t.Fatalf("observer expiration canceled recovery: %v", err)
	default:
	}
	running := r.call(t, "operation", dispatch.OperationArgs{OperationID: accepted.OperationID}).Data.(jobs.Status)
	if running.Operation.Kind != "reconcile" || running.Operation.State != ops.Preflight || running.Operation.PlanID != "" {
		t.Fatalf("bad durable receipt: %+v", running)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	settled := r.call(t, "operation", dispatch.OperationArgs{OperationID: accepted.OperationID}).Data.(jobs.Status)
	if settled.Operation.State != ops.Succeeded {
		t.Fatalf("recovery not successful: %+v", settled)
	}
	if err := r.runner.Run(worker, accepted.OperationID); err == nil {
		t.Fatal("terminal recovery job ran twice")
	}
}

func TestDetachedRecoveryLockTimeoutFailsWithoutInspection(t *testing.T) {
	for _, fence := range []string{"host", "launch"} {
		t.Run(fence, func(t *testing.T) {
			r := newDeployRig(t)
			accepted := r.call(t, "reconcile", dispatch.ReconcileArgs{DryRun: false}).Data.(jobs.Accepted)
			acquire := r.store.AcquireHostLock
			if fence == "launch" {
				acquire = r.store.AcquireLaunchLock
			}
			lock, err := acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Release()
			engine := r.server.Reconciler.(reconcile.Reconciler)
			engine.Systemd = &systemd.Fake{ShowFunc: func(context.Context, systemd.Unit) (systemd.Properties, error) {
				t.Fatal("recovery inspected without both locks")
				return systemd.Properties{}, nil
			}}
			r.runner.Recovery = recoveryJob(engine)
			worker, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if err := r.runner.Run(worker, accepted.OperationID); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ops.ErrLockUnavailable) {
				t.Fatalf("lock-only timeout not classified: %v", err)
			}
			status := r.call(t, "operation", dispatch.OperationArgs{OperationID: accepted.OperationID}).Data.(jobs.Status)
			if status.Operation.State != ops.Failed {
				t.Fatalf("lock-only recovery requires intervention: %+v", status.Operation)
			}
			failures := 0
			for _, event := range status.Events {
				if event.Kind != "failure" {
					continue
				}
				var failure ops.FailurePayload
				if err := json.Unmarshal(event.Payload, &failure); err != nil || failure.Code != "lock_unavailable" {
					t.Fatalf("bad lock failure: %+v error %v", event, err)
				}
				failures++
			}
			if failures != 1 {
				t.Fatalf("lock failures=%d, want 1", failures)
			}
		})
	}
}
