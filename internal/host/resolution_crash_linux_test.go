package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/reconcile"
)

func TestResolutionRunOpSIGKILLConvergesRemainingRemovalSteps(t *testing.T) {
	for _, step := range []string{"remove_unit", "reload_units", "retire_app", "phase:preflight", "phase:preparing", "phase:quiescing", "phase:starting", "phase:checking", "phase:committing"} {
		t.Run(step, func(t *testing.T) {
			dir := t.TempDir()
			h := newRemovalHost(t, dir, true)
			source := seedTerminalRemoval(t, h)
			accepted := h.call(t, "resolve", dispatch.ResolveArgs{OperationID: source, IdempotencyKey: "resolution-crash"}).Data.(jobs.Accepted)
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRemoveRunOpCrashChild$")
			child.Env = append(os.Environ(), "BRINE_REMOVE_CRASH_CHILD=1", "BRINE_REMOVE_CRASH_STATE="+dir, "BRINE_REMOVE_CRASH_OP="+accepted.OperationID, "BRINE_REMOVE_CRASH_STEP="+step)
			phase := strings.TrimPrefix(step, "phase:")
			phaseCrash := phase != step
			if phaseCrash {
				child.Env = append(child.Env, "BRINE_RESOLUTION_CRASH_PHASE="+phase)
			}
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
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			wait, stop := context.WithTimeout(ctx, 20*time.Millisecond)
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
			op, err := h.store.GetOperation(ctx, accepted.OperationID)
			if err != nil || op.State.IsTerminal() {
				t.Fatal(op, err)
			}
			if phaseCrash && op.State != ops.State(phase) {
				t.Fatalf("durable phase %s want %s", op.State, phase)
			}
			events, err := h.store.EventsAfter(ctx, accepted.OperationID, 0, 128)
			if err != nil {
				t.Fatal(err)
			}
			if phaseCrash {
				want := "stop_unit"
				if phase == "checking" {
					want = "remove_unit"
				}
				if phase == "committing" {
					want = "reload_units"
				}
				last := ops.StepPayload{}
				for _, event := range events {
					if event.Kind == "step" {
						if e := json.Unmarshal(event.Payload, &last); e != nil {
							t.Fatal(e)
						}
					}
				}
				if last.Step != want || last.Outcome == "intent" {
					t.Fatalf("phase write did not precede next step intent: %+v", last)
				}
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
			if !phaseCrash && !unknown {
				t.Fatal("interrupted effect was not journaled unknown")
			}
		})
	}
}
