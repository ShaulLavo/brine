//go:build linux

package localexec

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"testing"
	"time"
)

func TestForegroundHelper(t *testing.T) {
	path := os.Getenv("BRINE_FOREGROUND_READY")
	if path == "" {
		return
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	if err := os.WriteFile(path, []byte("ready"), 0600); err != nil { //nolint:gosec // Parent-selected temporary readiness file for this child-only test.
		t.Fatal(err)
	}
	select {
	case <-interrupt:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt deadline")
	}
}

func TestForegroundInterruptAndCancellationAreDistinct(t *testing.T) {
	for _, terminate := range []string{"interrupt", "cancel"} {
		t.Run(terminate, func(t *testing.T) { testForegroundTermination(t, terminate) })
	}
}

func testForegroundTermination(t *testing.T, terminate string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	directory := t.TempDir()
	path := filepath.Join(directory, "ready")
	process, err := StartForeground(ctx, ForegroundCommand{Path: os.Args[0], Args: []string{"-test.run=^TestForegroundHelper$"}, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + directory, "BRINE_FOREGROUND_READY=" + path}, Dir: directory})
	if err != nil {
		t.Fatal(err)
	}
	if process.PID() <= 0 || process.PID() == os.Getpid() {
		t.Fatal("foreground PID does not identify its own child")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err = os.Stat(path); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("helper never became ready")
		}
	}
	if terminate == "interrupt" {
		if err = process.Interrupt(); err != nil {
			t.Fatal(err)
		}
	} else {
		cancel()
	}
	select {
	case err = <-process.Done():
		if terminate == "interrupt" && err != nil || terminate == "cancel" && err == nil {
			t.Fatal("termination lost its outcome", terminate, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("foreground child survived termination")
	}
}
