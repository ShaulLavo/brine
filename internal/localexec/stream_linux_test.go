package localexec

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStreamDeadlineTerminatesDescendants(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pids")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := Command{Path: os.Args[0], Args: []string{"-test.run=^TestProcessTreeHelper$", "--", "BRINE_PROCESS_TREE_HELPER=1", "parent"}, Dir: filepath.Dir(pidfile), Env: []string{"PATH=/usr/bin:/bin", "BRINE_PROCESS_TREE_HELPER=1", "BRINE_PROCESS_TREE_PIDFILE=" + pidfile}, OutputLimit: 256, Mutation: true}
	err := (ExecRunner{}).RunStreams(ctx, command, nil, io.Discard)
	var operation *Error
	if !errors.As(err, &operation) || operation.Kind != UnknownOutcome {
		t.Fatal("stream mutation timeout was not unknown", err)
	}
	// #nosec G304 -- Exact disposable helper PID file under t.TempDir.
	raw, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		t.Fatal("descendant did not start")
	}
	for _, field := range fields {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		waitStreamProcessGone(t, pid)
	}
}

func waitStreamProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !processRunning(t, pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stream descendant survived cancellation")
}
