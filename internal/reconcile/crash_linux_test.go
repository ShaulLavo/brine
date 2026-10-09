//go:build linux

package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKilledRunnerChild$")
	child.Env = append(os.Environ(), "BRINE_RECONCILE_TEST_STATE="+dir, "BRINE_RECONCILE_TEST_OP="+op.ID)
	child.ExtraFiles = []*os.File{write}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.Process != nil {
			child.Process.Kill()
		}
	})
	write.Close()
	if err := read.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 5)
	if _, err := io.ReadFull(read, data); err != nil {
		t.Fatal(err)
	}
	r := Reconciler{Store: s, Systemd: absentRunner(), LockTimeout: 20 * time.Millisecond}
	if _, err := r.Reconcile(ctx); err == nil {
		t.Fatal("reconciled while runner held host lock")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("child was not killed")
	}
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
