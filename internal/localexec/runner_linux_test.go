package localexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcessTreeHelper(t *testing.T) {
	if os.Getenv("BRINE_PROCESS_TREE_HELPER") != "1" {
		return
	}
	if os.Args[len(os.Args)-1] == "child" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$", "--", "child")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	pids := fmt.Sprintf("%d %d", os.Getpid(), child.Process.Pid)
	if err := os.WriteFile(os.Getenv("BRINE_PROCESS_TREE_PIDFILE"), []byte(pids), 0600); err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		os.Exit(2)
	}
	_ = child.Wait()
	os.Exit(0)
}

func TestExecRunnerTimeoutTerminatesDescendants(t *testing.T) {
	t.Setenv("BRINE_PROCESS_TREE_HELPER", "1")
	pidfile := filepath.Join(t.TempDir(), "pids")
	t.Setenv("BRINE_PROCESS_TREE_PIDFILE", pidfile)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (ExecRunner{}).Run(ctx, os.Args[0], "-test.run=^TestProcessTreeHelper$", "--", "parent")
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
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 || len(data) < end+4 {
		t.Fatalf("invalid process stat: %q", data)
	}
	// Orphans may remain zombies until the host's init reaps them.
	return data[end+2] != 'Z' && data[end+2] != 'X'
}
