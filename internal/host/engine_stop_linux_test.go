//go:build linux

package host

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestEngineServiceSignalsOnlySupervisorBeforeForcedCleanup(t *testing.T) {
	permit, _ := engineStartupFixture(t)
	unit, err := EngineServiceBytes(permit.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "KillSignal=SIGINT\nKillMode=mixed\n") {
		t.Fatal("service can interrupt the native stop subprocess")
	}
	if strings.Contains(string(unit), "SendSIGKILL=no") {
		t.Fatal("service lost bounded forced cgroup cleanup")
	}
}

func TestEngineStopChildHelper(t *testing.T) {
	ready := os.Getenv("BRINE_ENGINE_STOP_READY")
	if ready == "" {
		return
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	if err := os.WriteFile(ready, []byte("ready"), 0600); err != nil { //nolint:gosec // Parent-selected readiness file for this child-only test.
		t.Fatal(err)
	}
	select {
	case <-interrupt:
	case <-time.After(5 * time.Second):
		t.Fatal("stop signal not delivered")
	}
	if os.Getenv("BRINE_ENGINE_STOP_OUTCOME") == "failure" {
		os.Exit(7)
	}
}

func TestEngineStopPreservesChildFailure(t *testing.T) {
	for _, outcome := range []string{"success", "failure"} {
		t.Run(outcome, func(t *testing.T) { testEngineStopOutcome(t, outcome) })
	}
}

func testEngineStopOutcome(t *testing.T, outcome string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	directory := t.TempDir()
	ready := filepath.Join(directory, "ready")
	process, err := localexec.StartForeground(ctx, localexec.ForegroundCommand{Path: os.Args[0], Args: []string{"-test.run=^TestEngineStopChildHelper$"}, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + directory, "BRINE_ENGINE_STOP_READY=" + ready, "BRINE_ENGINE_STOP_OUTCOME=" + outcome}, Dir: directory})
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err = os.Stat(ready); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("child never became ready")
		}
	}
	err = stopEngineChild(ctx, process, process.Done(), time.Second)
	if outcome == "failure" && err == nil {
		t.Fatal("requested stop concealed the native child failure")
	}
	if outcome == "success" && err != nil {
		t.Fatal("clean child stop refused", err)
	}
}
