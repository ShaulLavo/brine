package localexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcessTreeHelper(t *testing.T) {
	if os.Getenv("BRINE_PROCESS_TREE_HELPER") != "1" && !slices.Contains(os.Args, "BRINE_PROCESS_TREE_HELPER=1") {
		return
	}
	if os.Args[len(os.Args)-1] == "signal" {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(time.Second)
		os.Exit(2)
	}
	if os.Args[len(os.Args)-1] == "child" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$", "--", "BRINE_PROCESS_TREE_HELPER=1", "child")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	pids := fmt.Sprintf("%d %d", os.Getpid(), child.Process.Pid)
	pidfile := os.Getenv("BRINE_PROCESS_TREE_PIDFILE")
	for _, arg := range os.Args {
		if value, ok := strings.CutPrefix(arg, "BRINE_PROCESS_TREE_PIDFILE="); ok {
			pidfile = value
		}
	}
	if err := os.WriteFile(pidfile, []byte(pids), 0600); err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		os.Exit(2)
	}
	_ = child.Wait()
	os.Exit(0)
}

func TestExecRunnerTimeoutTerminatesDescendants(t *testing.T) {
	testTimeoutTerminatesDescendants(t, false)
}

func TestExecuteTimeoutTerminatesDescendants(t *testing.T) {
	testTimeoutTerminatesDescendants(t, true)
}

func testTimeoutTerminatesDescendants(t *testing.T, execute bool) {
	t.Helper()
	t.Setenv("BRINE_PROCESS_TREE_HELPER", "1")
	pidfile := filepath.Join(t.TempDir(), "pids")
	t.Setenv("BRINE_PROCESS_TREE_PIDFILE", pidfile)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var err error
		if execute {
			_, err = (ExecRunner{}).Execute(ctx, Command{Path: os.Args[0], Args: []string{"-test.run=^TestProcessTreeHelper$", "--", "BRINE_PROCESS_TREE_HELPER=1", "BRINE_PROCESS_TREE_PIDFILE=" + pidfile, "parent"}, Timeout: 3 * time.Second, Mutation: true})
		} else {
			_, err = (ExecRunner{}).Run(ctx, os.Args[0], "-test.run=^TestProcessTreeHelper$", "--", "BRINE_PROCESS_TREE_HELPER=1", "BRINE_PROCESS_TREE_PIDFILE="+pidfile, "parent")
		}
		done <- err
	}()
	var pids []int
	defer func() {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidfile)
		if err == nil {
			for _, field := range strings.Fields(string(data)) {
				pid, err := strconv.Atoi(field)
				if err != nil {
					t.Fatal(err)
				}
				pids = append(pids, pid)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(pids) != 2 {
		t.Fatal("probe did not start its descendant")
	}
	for _, pid := range pids {
		if !processRunning(t, pid) {
			t.Fatalf("fixture process %d was not running before timeout", pid)
		}
	}
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !processRunning(t, pids[0]) && !processRunning(t, pids[1]) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("probe or its descendant survived the deadline")
}

func processRunning(t *testing.T, pid int) bool {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	running, err := processStatRunning(data, err)
	if err != nil {
		t.Fatal(err)
	}
	return running
}

func processStatRunning(data []byte, err error) (bool, error) {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 || len(data) < end+4 {
		return false, fmt.Errorf("invalid process stat: %q", data)
	}
	// Orphans may remain zombies until the host's init reaps them.
	return data[end+2] != 'Z' && data[end+2] != 'X', nil
}

func TestProcessStatRunning(t *testing.T) {
	for _, tt := range []struct {
		name      string
		data      string
		err       error
		running   bool
		wantError bool
	}{
		{"running", "123 (probe) S 1", nil, true, false},
		{"zombie", "123 (probe) Z 1", nil, false, false},
		{"dead", "123 (probe) X 1", nil, false, false},
		{"missing path", "", &os.PathError{Op: "open", Path: "/proc/123/stat", Err: syscall.ENOENT}, false, false},
		{"reaped during read", "", &os.PathError{Op: "read", Path: "/proc/123/stat", Err: syscall.ESRCH}, false, false},
		{"no process", "", syscall.ESRCH, false, false},
		{"permission denied", "", syscall.EACCES, false, true},
		{"invalid stat", "invalid", nil, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			running, err := processStatRunning([]byte(tt.data), tt.err)
			if running != tt.running || (err != nil) != tt.wantError {
				t.Fatalf("running = %t, error = %v", running, err)
			}
		})
	}
}

func TestExecuteSignalLeavesMutationOutcomeUnknown(t *testing.T) {
	t.Setenv("BRINE_PROCESS_TREE_HELPER", "1")
	for _, mutation := range []bool{false, true} {
		_, err := (ExecRunner{}).Execute(context.Background(), Command{Path: os.Args[0], Args: []string{"-test.run=^TestProcessTreeHelper$", "--", "BRINE_PROCESS_TREE_HELPER=1", "signal"}, Timeout: 3 * time.Second, Mutation: mutation})
		var re *Error
		want := Failed
		if mutation {
			want = UnknownOutcome
		}
		if !errors.As(err, &re) || re.Kind != want || re.ExitCode != -1 {
			t.Fatal(err)
		}
	}
}
