//go:build linux

package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestKilledRunnerChild(t *testing.T) {
	dir := os.Getenv("BRINE_RECONCILE_TEST_STATE")
	if dir == "" {
		return
	}
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	lock, err := s.AcquireHostLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	id := os.Getenv("BRINE_RECONCILE_TEST_OP")
	if err := s.SetOperationState(context.Background(), id, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(context.Background(), id, ops.Preparing); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"preflight", "pull_image", "verify_image", "ensure_secrets", "stage_unit"} {
		outcome := "completed"
		if step == "stage_unit" {
			outcome = "intent"
		}
		data, _ := json.Marshal(ops.StepPayload{Step: step, Outcome: outcome})
		if _, err := s.AppendEvent(context.Background(), id, ops.Event{Kind: "step", Payload: data}); err != nil {
			t.Fatal(err)
		}
	}
	ready := os.NewFile(3, "ready")
	if _, err := ready.Write([]byte("ready")); err != nil {
		t.Fatal(err)
	}
	ready.Close()
	// Stand in for a blocking external effect after its durable intent.
	<-time.After(time.Minute)
}

func TestSIGKILLReleasesHostLockAndReconcileConverges(t *testing.T) {
	s, op, dir := fixture(t)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	childCtx, cancelChild := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelChild()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- The helper is this test binary, resolved by os.Executable.
	child := exec.CommandContext(childCtx, binary, "-test.run=^TestKilledRunnerChild$")
	child.Env = append(os.Environ(), "BRINE_RECONCILE_TEST_STATE="+dir, "BRINE_RECONCILE_TEST_OP="+op.ID)
	child.ExtraFiles = []*os.File{write}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	write.Close()
	deadline, _ := childCtx.Deadline()
	if err := read.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 5)
	if _, err := io.ReadFull(read, data); err != nil {
		t.Fatal(err)
	}
	liveCtx, cancelLive := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelLive()
	if lock, err := s.TryAcquireHostLock(liveCtx); !errors.Is(err, ops.ErrLockUnavailable) {
		if lock != nil {
			_ = lock.Release()
		}
		t.Fatalf("live runner did not hold host lock: %v", err)
	}
	r := Reconciler{Store: s, Systemd: absentRunner(), LockTimeout: 20 * time.Millisecond}
	if _, err := r.Reconcile(liveCtx); !errors.Is(err, ops.ErrLockUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("live runner did not exclude reconciliation: %v", err)
	}
	cancelLive()
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = child.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("child was not killed: %v", err)
	}
	status := exit.Sys().(syscall.WaitStatus)
	if !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child did not exit from SIGKILL: %v", status)
	}
	cancelChild()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lock, err := s.TryAcquireHostLock(ctx)
	if err != nil {
		t.Fatalf("SIGKILL left host lock unavailable: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	// The refusal probe's tiny budget must not cover filesystem work in recovery.
	r.LockTimeout = 5 * time.Second
	report, err := r.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Outcomes) != 1 || report.Outcomes[0].After != ops.RecoveryRequired {
		t.Fatalf("report %+v", report)
	}
	report, err = r.Reconcile(ctx)
	if err != nil || len(report.Outcomes) != 0 {
		t.Fatalf("report %+v error %v", report, err)
	}
}
