package localexec

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

var capturedAdapters = []string{"Run", "RunStdout", "CaptureStdout", "Execute", "ExecuteMutation", "RunInput", "RunStreams"}

func TestCapturedParentExitTerminatesDescendants(t *testing.T) {
	for _, adapter := range capturedAdapters {
		t.Run(adapter, func(t *testing.T) { testCapturedParentExit(t, adapter) })
	}
}

func testCapturedParentExit(t *testing.T, adapter string) {
	t.Helper()
	for _, mode := range []string{"exit-success-inherited", "exit-failure-inherited", "exit-success-closed", "exit-failure-closed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			command, pids := capturedTreeCommand(t, mode)
			err := invokeCapturedAdapter(ctx, adapter, command)
			assertCapturedExit(t, adapter, mode, err)
			assertCapturedTreeStopped(t, pids())
		})
	}
}

func TestCapturedDeadlineTerminatesDescendants(t *testing.T) {
	for _, adapter := range capturedAdapters {
		t.Run(adapter, func(t *testing.T) {
			command, pids := capturedTreeCommand(t, "parent")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := invokeCapturedAdapter(ctx, adapter, command)
			assertCapturedDeadline(t, adapter, err)
			assertCapturedTreeStopped(t, pids())
		})
	}
}

func TestCapturedFailedStart(t *testing.T) {
	for _, adapter := range capturedAdapters {
		t.Run(adapter, func(t *testing.T) {
			directory := t.TempDir()
			command := Command{Path: filepath.Join(directory, "missing"), Dir: directory, Timeout: time.Second, OutputLimit: 256}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := invokeCapturedAdapter(ctx, adapter, command)
			assertCapturedFailedStart(t, adapter, err)
		})
	}
}

func invokeCapturedAdapter(ctx context.Context, adapter string, command Command) error {
	runner := ExecRunner{}
	switch adapter {
	case "Run":
		_, err := runner.Run(ctx, command.Path, command.Args...)
		return err
	case "RunStdout":
		_, err := runner.RunStdout(ctx, command.Path, command.Args...)
		return err
	case "CaptureStdout":
		_, err := runner.CaptureStdout(ctx, command.OutputLimit, command.Path, command.Args...)
		return err
	case "Execute", "ExecuteMutation":
		command.Mutation = adapter == "ExecuteMutation"
		_, err := runner.Execute(ctx, command)
		return err
	case "RunInput":
		_, err := runner.RunInput(ctx, command)
		return err
	case "RunStreams":
		command.Env = []string{"PATH=/usr/bin:/bin"}
		return runner.RunStreams(ctx, command, nil, io.Discard)
	default:
		panic("unknown captured adapter")
	}
}

func capturedTreeCommand(t *testing.T, mode string) (Command, func() []int) {
	t.Helper()
	pidfile := filepath.Join(t.TempDir(), "pids")
	readPIDs := func() []int { return capturedTreePIDs(t, pidfile) }
	// Register before launch so even assertion failures clean up the exact fixture.
	t.Cleanup(func() {
		for _, pid := range readPIDs() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		assertCapturedTreeStopped(t, readPIDs())
	})
	command := Command{
		Path: os.Args[0], Args: []string{"-test.run=^TestProcessTreeHelper$", "--", "BRINE_PROCESS_TREE_HELPER=1", "BRINE_PROCESS_TREE_PIDFILE=" + pidfile, mode},
		Dir: filepath.Dir(pidfile), Timeout: 3 * time.Second, OutputLimit: 256,
	}
	return command, readPIDs
}

func capturedTreePIDs(t *testing.T, pidfile string) []int {
	t.Helper()
	// #nosec G304 -- Exact disposable helper PID file under t.TempDir.
	raw, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal("helper did not record its descendant", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		t.Fatal("helper did not record two process IDs")
	}
	pids := make([]int, 0, 2)
	for _, field := range fields {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			t.Fatal("invalid helper process ID", field)
		}
		pids = append(pids, pid)
	}
	return pids
}

func assertCapturedTreeStopped(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		waitStreamProcessGone(t, pid)
	}
}

func assertCapturedExit(t *testing.T, adapter, mode string, err error) {
	t.Helper()
	failure := strings.HasPrefix(mode, "exit-failure")
	inherited := strings.HasSuffix(mode, "inherited")
	if !failure && !inherited {
		if err != nil {
			t.Fatal("closed-pipe success", err)
		}
		return
	}
	if adapter == "Execute" || adapter == "ExecuteMutation" || adapter == "RunStreams" {
		kind := Failed
		if adapter == "ExecuteMutation" && !failure {
			kind = UnknownOutcome
		}
		assertCapturedErrorKind(t, err, kind)
		return
	}
	if !failure {
		if !errors.Is(err, exec.ErrWaitDelay) {
			t.Fatal("inherited-pipe success must retain wait/drain failure", err)
		}
		return
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatal("parent failure must retain exit code 7", err)
	}
}

func assertCapturedDeadline(t *testing.T, adapter string, err error) {
	t.Helper()
	if adapter == "ExecuteMutation" {
		assertCapturedErrorKind(t, err, UnknownOutcome)
		return
	}
	if adapter == "Execute" || adapter == "RunStreams" {
		assertCapturedErrorKind(t, err, Timeout)
		return
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("deadline error", err)
	}
}

func assertCapturedFailedStart(t *testing.T, adapter string, err error) {
	t.Helper()
	if adapter == "Execute" || adapter == "ExecuteMutation" || adapter == "RunStreams" {
		assertCapturedErrorKind(t, err, Failed)
		return
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed start error", err)
	}
}

func assertCapturedErrorKind(t *testing.T, err error, kind ErrorKind) {
	t.Helper()
	var operation *Error
	if !errors.As(err, &operation) || operation.Kind != kind {
		t.Fatalf("error = %v, want %s", err, kind)
	}
}
