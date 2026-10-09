//go:build linux

package jobs_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type contendedStore struct {
	disconnectStore
	locker *store.Store
}

func (s contendedStore) AcquireHostLock(ctx context.Context) (ops.Lock, error) {
	return s.locker.AcquireHostLock(ctx)
}

type countLaunch struct{ calls int }

func (l *countLaunch) Launch(context.Context, systemd.OperationID) error {
	l.calls++
	return nil
}

type forbidExecution struct{ t *testing.T }

func (e forbidExecution) Run(context.Context, string, plan.Plan, policy.Desired) error {
	e.t.Fatal("executed without the host lock")
	return nil
}

func TestHostLockProcessHelper(t *testing.T) {
	dir := os.Getenv("BRINE_LOCK_HELPER_DIR")
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
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestRunnerLockUnavailableAfterAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHostLockProcessHelper$")
	child.Env = append(os.Environ(), "BRINE_LOCK_HELPER_DIR="+dir)
	child.Stderr = os.Stderr
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = child.Process.Kill()
		_ = child.Wait()
	}()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "locked" {
		t.Fatal("lock holder did not become ready")
	}
	fixture := contendedStore{disconnectStore: disconnectStore{dir: t.TempDir()}, locker: s}
	launcher := &countLaunch{}
	service := jobs.Service{Store: fixture, Launcher: launcher, Requester: "runner"}
	planID := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	accepted, err := service.Apply(context.Background(), planID, "key1")
	if err != nil {
		t.Fatal(err)
	}
	runner := jobs.Runner{Store: fixture, Executor: forbidExecution{t}, LockWaitTimeout: 40 * time.Millisecond}
	if err = runner.Run(ctx, accepted.OperationID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock deadline not returned: %v", err)
	}
	status, err := service.Operation(context.Background(), accepted.OperationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range status.Events {
		if event.Kind == "failure" {
			var payload ops.FailurePayload
			if err = json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			found = payload.Code == "lock_unavailable"
		}
	}
	if status.Operation.State != ops.Failed || !found {
		t.Fatalf("lock failure was not journaled: %+v", status)
	}
	again, err := service.Apply(context.Background(), planID, "key1")
	if err != nil || again != accepted || launcher.calls != 1 {
		t.Fatalf("retry relaunched: accepted=%+v calls=%d err=%v", again, launcher.calls, err)
	}
}
