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
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/reconcile"
)

func TestRemoveRunOpCrashChild(t *testing.T) {
	if os.Getenv("BRINE_REMOVE_CRASH_CHILD") != "1" {
		return
	}
	h := newRemovalHost(t, os.Getenv("BRINE_REMOVE_CRASH_STATE"), false)
	if phase := os.Getenv("BRINE_RESOLUTION_CRASH_PHASE"); phase != "" {
		engine := h.runner.Executor.(Executor).Engine
		engine.Journal = phaseCrashJournal{Journal: engine.Journal, phase: ops.State(phase)}
		h.runner.Reconciler = runnerReconciler{newReconciler(h.service, engine, engine.Systemd)}
	}
	h.boundary = func(step string) {
		if step != os.Getenv("BRINE_REMOVE_CRASH_STEP") {
			return
		}
		pipe := os.NewFile(3, "ready")
		fmt.Fprintln(pipe, "ready")
		pipe.Close()
		select {}
	}
	if err := h.runner.Run(context.Background(), os.Getenv("BRINE_REMOVE_CRASH_OP")); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash boundary not reached")
}
func TestRemoveRunOpSIGKILLConvergesEveryEffectBoundary(t *testing.T) {
	for _, step := range []string{"withdraw_route", "stop_unit", "remove_unit", "reload_units", "retire_app"} {
		t.Run(step, func(t *testing.T) {
			dir := t.TempDir()
			h := newRemovalHost(t, dir, true)
			planned := h.call(t, "lifecycle", dispatch.LifecycleArgs{App: "hello", Action: plan.RemoveApp}).Data.(apps.ConfigPlan)
			accepted := h.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: "remove-crash"}).Data.(jobs.Accepted)
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			childCtx, childCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer childCancel()
			child := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestRemoveRunOpCrashChild$")
			child.Env = append(os.Environ(), "BRINE_REMOVE_CRASH_CHILD=1", "BRINE_REMOVE_CRASH_STATE="+dir, "BRINE_REMOVE_CRASH_OP="+accepted.OperationID, "BRINE_REMOVE_CRASH_STEP="+step)
			child.ExtraFiles = []*os.File{write}
			if err = child.Start(); err != nil {
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
					err = fmt.Errorf("invalid barrier")
				}
				ready <- err
			}()
			select {
			case err = <-ready:
				if err != nil {
					t.Fatal(err)
				}
			case <-childCtx.Done():
				t.Fatal(childCtx.Err())
			}
			wait, stop := context.WithTimeout(childCtx, 20*time.Millisecond)
			lock, lockErr := h.store.AcquireHostLock(wait)
			stop()
			if lockErr == nil {
				lock.Release()
				t.Fatal("run-op did not hold mutation lock")
			}
			if err = child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = child.Wait(); err == nil {
				t.Fatal("child survived kill")
			}
			status, ok := child.ProcessState.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatal(child.ProcessState)
			}
			childCancel()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			op, err := h.store.GetOperation(ctx, accepted.OperationID)
			if err != nil || op.State.IsTerminal() {
				t.Fatal(op, err)
			}
			events, err := h.store.EventsAfter(ctx, accepted.OperationID, 0, 128)
			if err != nil {
				t.Fatal(err)
			}
			disk, err := h.read()
			if err != nil {
				t.Fatal(err)
			}
			preview := h.call(t, "reconcile", dispatch.ReconcileArgs{DryRun: true}).Data.(reconcile.Report)
			if len(preview.Outcomes) != 1 || preview.Outcomes[0].After != ops.Succeeded {
				t.Fatal(preview)
			}
			afterDisk, _ := h.read()
			afterEvents, _ := h.store.EventsAfter(ctx, accepted.OperationID, 0, 128)
			if !reflect.DeepEqual(disk, afterDisk) || !reflect.DeepEqual(events, afterEvents) {
				t.Fatal("dry-run changed host or journal")
			}
			recovery := h.call(t, "reconcile", dispatch.ReconcileArgs{}).Data.(jobs.Accepted)
			if err = h.runner.Run(ctx, recovery.OperationID); err != nil {
				t.Fatal(err)
			}
			settled, err := h.store.GetOperation(ctx, accepted.OperationID)
			if err != nil || settled.State != ops.Succeeded {
				t.Fatal(settled, err)
			}
			disk, err = h.read()
			if err != nil || disk.Active || !disk.Reloaded || !disk.RouteReloaded || len(*disk.Snapshot.Apps.Value) != 0 || len(disk.Snapshot.CaddyConfig.Value.Files) != 0 {
				t.Fatal(disk, err)
			}
			state, err := h.store.LoadBrineState(ctx, disk.Snapshot.Identity, 2)
			if err != nil || len(state.Releases) != 0 {
				t.Fatal(state, err)
			}
			again, err := h.server.Reconciler.Reconcile(ctx)
			if err != nil || len(again.Outcomes) != 0 {
				t.Fatal(again, err)
			}
			finalEvents, err := h.store.EventsAfter(ctx, accepted.OperationID, 0, 128)
			if err != nil {
				t.Fatal(err)
			}
			unknown := false
			for _, event := range finalEvents {
				var p ops.StepPayload
				if event.Kind == "step" && json.Unmarshal(event.Payload, &p) == nil && p.Step == step && p.Outcome == "unknown" {
					unknown = true
				}
			}
			if !unknown {
				t.Fatal("interrupted effect was not journaled unknown")
			}
		})
	}
}

// Stop exactly after the durable phase write, before step intent can be appended.
type phaseCrashJournal struct {
	apply.Journal
	phase ops.State
}

func (j phaseCrashJournal) SetOperationState(ctx context.Context, id string, state ops.State) error {
	if err := j.Journal.SetOperationState(ctx, id, state); err != nil {
		return err
	}
	if state == j.phase {
		pipe := os.NewFile(3, "ready")
		fmt.Fprintln(pipe, "ready")
		pipe.Close()
		select {}
	}
	return nil
}
