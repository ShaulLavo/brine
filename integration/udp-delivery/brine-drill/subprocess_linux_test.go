package brinedrill

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestExecRunnerDeadlineStopsFakeCLI(t *testing.T) {
	testFakeCLIStop(t, false)
}

func TestExecRunnerInterruptionStopsFakeCLI(t *testing.T) {
	testFakeCLIStop(t, true)
}

func testFakeCLIStop(t *testing.T, interrupt bool) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(t.TempDir(), "fake-cli.pid")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	pid := 0
	t.Cleanup(func() { stopFakeCLI(t, cancel, done, pid) })
	go func() {
		_, err := (ExecRunner{}).Run(ctx, CommandSpec{Binary: binary, Args: []string{"-test.run=^TestDrillFakeCLI$", "--", "brine-drill-fake", pidPath}, Mutating: interrupt}, time.Second, 1024)
		done <- err
		close(done)
	}()
	pid = waitFakeCLIPID(t, pidPath)
	if interrupt {
		cancel()
	}
	select {
	case err := <-done:
		var commandErr *localexec.Error
		if !errors.As(err, &commandErr) {
			t.Fatalf("fake CLI error = %v", err)
		}
		want := localexec.Timeout
		if interrupt {
			want = localexec.UnknownOutcome
		}
		if commandErr.Kind != want {
			t.Fatalf("fake CLI error kind = %s, want %s", commandErr.Kind, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fake CLI exceeded its command deadline")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("fake CLI process %d remains after return: %v", pid, err)
	}
}

func stopFakeCLI(t *testing.T, cancel context.CancelFunc, done <-chan error, pid int) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Error("fake CLI did not stop during test teardown")
	}
}

func waitFakeCLIPID(t *testing.T, path string) int {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		raw, err := root.ReadFile("fake-cli.pid")
		pid, parseErr := strconv.Atoi(string(raw))
		if err == nil && parseErr == nil && pid > 0 {
			return pid
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("fake CLI did not signal startup")
		}
	}
}

func TestDrillFakeCLI(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "brine-drill-fake" {
		return
	}
	root, err := os.OpenRoot(filepath.Dir(os.Args[len(os.Args)-1]))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := root.WriteFile("fake-cli.pid", []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Hour)
}
