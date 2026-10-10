//go:build linux

package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
)

type crashUnits struct {
	*fakeUnits
	barrier func()
}

func (u crashUnits) Stage(context.Context, quadlet.Unit) error { u.barrier(); return nil }

func TestRunOpCrashChild(t *testing.T) {
	if os.Getenv("BRINE_CRASH_CHILD") != "1" {
		return
	}
	r := newDeployRigAt(t, os.Getenv("BRINE_CRASH_STATE"))
	barrier := func() {
		ready := os.NewFile(3, "ready")
		fmt.Fprintln(ready, "ready")
		ready.Close()
		select {}
	}
	executor := r.runner.Executor.(Executor)
	if os.Getenv("BRINE_CRASH_STEP") == "pull_image" {
		executor.Engine.Podman.(*podman.Fake).PullFunc = func(context.Context, podman.Image) error { barrier(); return nil }
	} else {
		executor.Engine.Units = crashUnits{r.units, barrier}
	}
	r.runner.Executor = executor
	if err := r.runner.Run(context.Background(), os.Getenv("BRINE_CRASH_OP")); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash boundary was not reached")
}

func TestDispatcherReconcilesCrashedRunOp(t *testing.T) {
	for _, step := range []string{"pull_image", "stage_unit"} {
		t.Run(step, func(t *testing.T) {
			dir := t.TempDir()
			r := newDeployRigAt(t, dir)
			r.runner.Executor.(Executor).Engine.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
				return podman.ContainerState{}, &localexec.Error{Kind: localexec.NotFound}
			}
			factory := newServerFactory("fixture", "deploy", func(context.Context, string) (*Runtime, error) {
				return &Runtime{Inventory: r.inventory, Planner: r.service, Jobs: jobs.Service{Store: r.store, Launcher: r.launcher, Requester: r.service.Requester}, Reconciler: r.server.Reconciler, Authorize: r.service.Authorize, close: func() error { return nil }}, nil
			}, nil)
			factory.diagnosticStateDir = dir
			factory.previewOpen = func(ctx context.Context, _ string) (*Runtime, error) {
				preview, err := openPreviewState(ctx, dir)
				if err != nil || preview.previewStore == nil {
					return preview, err
				}
				collector := *r.inventory
				collector.store = preview.previewStore
				service := r.service
				service.Store = preview.previewStore
				service.Inventory = &collector
				engine := r.runner.Executor.(Executor).Engine
				engine.Journal = preview.previewStore
				engine.Plans = preview.previewStore
				engine.Releases = releases{preview.previewStore}
				preview.Reconciler = readOnlyReconciler{newReconciler(service, engine, engine.Systemd)}
				preview.Authorize = service.Authorize
				return preview, nil
			}
			r.server.Factory = factory.Build
			defer factory.Close()
			planned := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
			accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: "crash-key"}).Data.(jobs.Accepted)
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			childCtx, childCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer childCancel()
			child := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestRunOpCrashChild$")
			child.Env = append(os.Environ(), "BRINE_CRASH_CHILD=1", "BRINE_CRASH_STATE="+dir, "BRINE_CRASH_OP="+accepted.OperationID, "BRINE_CRASH_STEP="+step)
			child.ExtraFiles = []*os.File{write}
			if err := child.Start(); err != nil {
				write.Close()
				t.Fatal(err)
			}
			write.Close()
			t.Cleanup(func() {
				if child.ProcessState == nil {
					child.Process.Kill()
					child.Wait()
				}
			})
			ready := make(chan error, 1)
			go func() {
				var word string
				_, err := fmt.Fscanln(read, &word)
				if err == nil && word != "ready" {
					err = fmt.Errorf("bad barrier")
				}
				ready <- err
			}()
			select {
			case err := <-ready:
				if err != nil {
					t.Fatal(err)
				}
			case <-childCtx.Done():
				t.Fatal(childCtx.Err())
			}
			wait, stop := context.WithTimeout(childCtx, 20*time.Millisecond)
			lock, lockErr := r.store.AcquireHostLock(wait)
			stop()
			if lockErr == nil {
				lock.Release()
				t.Fatal("runner did not hold the host lock at crash boundary")
			}
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := child.Wait(); err == nil {
				t.Fatal("child not killed")
			}
			killed, ok := child.ProcessState.Sys().(syscall.WaitStatus)
			if !ok || !killed.Signaled() || killed.Signal() != syscall.SIGKILL {
				t.Fatalf("child exited without SIGKILL: %v", child.ProcessState)
			}
			childCancel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			before, err := r.store.GetOperation(ctx, accepted.OperationID)
			if err != nil || before.State != ops.Preparing {
				t.Fatalf("interrupted operation %+v error %v", before, err)
			}
			eventsBefore, err := r.store.EventsAfter(ctx, accepted.OperationID, 0, 128)
			if err != nil {
				t.Fatal(err)
			}
			filesBefore := previewFiles(t, dir)
			preview := r.call(t, "reconcile", dispatch.ReconcileArgs{DryRun: true}).Data.(reconcile.Report)
			if err := factory.Close(); err != nil {
				t.Fatal(err)
			}
			if after := previewFiles(t, dir); !reflect.DeepEqual(filesBefore, after) {
				t.Fatal("crash preview changed control files")
			}
			if len(preview.Outcomes) != 1 || !preview.DryRun {
				t.Fatalf("preview %+v", preview)
			}
			unchanged, err := r.store.GetOperation(ctx, accepted.OperationID)
			if err != nil || unchanged.State != before.State {
				t.Fatal("preview changed state")
			}
			eventsAfter, err := r.store.EventsAfter(ctx, accepted.OperationID, 0, 128)
			if err != nil || len(eventsBefore) != len(eventsAfter) {
				t.Fatal("preview changed journal")
			}
			recoveryAccepted := r.call(t, "reconcile", dispatch.ReconcileArgs{DryRun: false}).Data.(jobs.Accepted)
			worker, cancelWorker := context.WithTimeout(context.Background(), time.Minute)
			defer cancelWorker()
			if step == "pull_image" {
				engine := r.server.Reconciler.(reconcile.Reconciler)
				factory := engine.ExecutorFor
				engine.ExecutorFor = func(ctx context.Context, op ops.Operation, p plan.Plan, d policy.Desired) (*apply.Executor, error) {
					e, err := factory(ctx, op, p, d)
					if err == nil {
						e.Health = deadlineHealth{r.health}
					}
					return e, err
				}
				r.runner.Recovery = recoveryJob(engine)
			}
			runErr := r.runner.Run(worker, recoveryAccepted.OperationID)
			want := ops.RecoveryRequired
			if step == "pull_image" {
				want = ops.Succeeded
			}
			cancel()
			cancelWorker()
			observer, cancelObserver := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelObserver()
			recovered, err := r.store.GetOperation(observer, accepted.OperationID)
			if err != nil || recovered.State != want {
				t.Fatalf("state %+v want %s error %v", recovered, want, err)
			}
			if step == "pull_image" && runErr != nil || step == "stage_unit" && result.Classify(runErr).Code() != result.RecoveryRequired {
				t.Fatalf("recovery job result %v", runErr)
			}
			receipt := r.call(t, "operation", dispatch.OperationArgs{OperationID: recoveryAccepted.OperationID}).Data.(jobs.Status)
			if receipt.Operation.Kind != "reconcile" || receipt.Operation.State != want {
				t.Fatalf("recovery receipt %+v", receipt)
			}
			if r.pulls != 0 || len(r.launcher.ids) != 2 || r.launcher.ids[1] != recoveryAccepted.OperationID || recoveryAccepted.OperationID == accepted.OperationID {
				t.Fatal("replayed pull or launched a replacement runner")
			}
			if step == "stage_unit" && (r.units.installs != 0 || r.health.calls != 0) {
				t.Fatal("unknown stage replayed")
			}
			if step == "pull_image" && (r.units.installs != 1 || r.health.calls != 2) {
				t.Fatal("proven prefix did not resume suffix")
			}
			settled := r.call(t, "operation", dispatch.OperationArgs{OperationID: accepted.OperationID}).Data.(jobs.Status)
			var pullIntents int
			for _, event := range settled.Events {
				var effect ops.StepPayload
				if event.Kind == "step" && json.Unmarshal(event.Payload, &effect) == nil && effect.Step == "pull_image" && effect.Outcome == "intent" {
					pullIntents++
				}
			}
			if pullIntents != 1 {
				t.Fatalf("pull intent count %d", pullIntents)
			}
			again, err := r.server.Reconciler.Reconcile(observer)
			if err != nil {
				t.Fatal(err)
			}
			if len(again.Outcomes) != 0 {
				t.Fatal("terminal operation did not converge")
			}
		})
	}
}

// A resumed health wait gets the executor's own effect bound, not the remote
// observer's fifteen-second request deadline. No sleep is needed to prove it.
type deadlineHealth struct{ inner *fakeHealth }

func (h deadlineHealth) Check(ctx context.Context, d policy.Desired, p target.Port, routed bool) error {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 15*time.Second {
		return context.DeadlineExceeded
	}
	return h.inner.Check(ctx, d, p, routed)
}
