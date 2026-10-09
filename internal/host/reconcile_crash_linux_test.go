//go:build linux

package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/reconcile"
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
			factory := newServerFactory("fixture", "deploy", func(context.Context, string) (*Runtime, error) {
				return &Runtime{Inventory: r.inventory, Planner: r.service, Jobs: jobs.Service{Store: r.store, Launcher: r.launcher, Requester: r.service.Requester}, Reconciler: r.server.Reconciler, Authorize: r.service.Authorize, close: func() error { return nil }}, nil
			}, nil)
			factory.previewOpen = factory.open
			r.server.Factory = factory.Build
			defer factory.Close()
			planned := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
			accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: "crash-key"}).Data.(jobs.Accepted)
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunOpCrashChild$")
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
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			wait, stop := context.WithTimeout(ctx, 20*time.Millisecond)
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
			before, err := r.store.GetOperation(ctx, accepted.OperationID)
			if err != nil || before.State != ops.Preparing {
				t.Fatalf("interrupted operation %+v error %v", before, err)
			}
			eventsBefore, err := r.store.EventsAfter(ctx, accepted.OperationID, 0, 128)
			if err != nil {
				t.Fatal(err)
			}
			preview := r.call(t, "reconcile", dispatch.ReconcileArgs{DryRun: true}).Data.(reconcile.Report)
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
			report := r.call(t, "reconcile", dispatch.ReconcileArgs{DryRun: false}).Data.(reconcile.Report)
			want := ops.RecoveryRequired
			if step == "pull_image" {
				want = ops.Succeeded
			}
			if len(report.Outcomes) != 1 || report.Outcomes[0].After != want {
				t.Fatalf("report %+v want %s", report, want)
			}
			if r.pulls != 0 || len(r.launcher.ids) != 1 {
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
			again := r.call(t, "reconcile", dispatch.ReconcileArgs{DryRun: false}).Data.(reconcile.Report)
			if len(again.Outcomes) != 0 {
				t.Fatal("terminal operation did not converge")
			}
		})
	}
}
