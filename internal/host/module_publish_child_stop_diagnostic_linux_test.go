//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestModuleNativeDiagnosticsKeepOnlyBoundedMarkerState(t *testing.T) {
	diagnostics := &moduleNativeDiagnostics{}
	for _, chunk := range [][]byte{bytes.Repeat([]byte("PRIVATE_SECRET"), 10000), []byte("thread worker pani"), []byte("cked at PRIVATE_NATIVE_PATH TOKEN")} {
		if n, err := diagnostics.Write(chunk); err != nil || n != len(chunk) {
			t.Fatal("bounded sink changed subprocess write result", err)
		}
	}
	if !diagnostics.panicSeen() || len(diagnostics.tail) > 64 {
		t.Fatal("split late panic marker missed or unbounded tail retained")
	}
	var frame bytes.Buffer
	if err := emitModuleStageDiagnostic(&frame, "guest_child_stop_panic"); err != nil || bytes.Contains(frame.Bytes(), []byte("PRIVATE")) || decodeModuleStageDiagnostic(frame.Bytes()) != "guest_child_stop_panic" {
		t.Fatal("bounded diagnosis leaked native content", err)
	}
}

func TestModuleChildStopReasonsKeepWaitCauseAndNeverAcceptFailure(t *testing.T) {
	for _, phase := range []string{"interrupt", "context", "budget"} {
		original := engineChildStopError{phase: phase, cause: errEngineSource}
		if got := moduleChildStopFailureCode(original, false); got != "guest_child_stop_"+phase || !errors.Is(original, errEngineSource) {
			t.Fatal("stop phase or cause lost", phase, got)
		}
	}
	for _, status := range []struct {
		name, code string
		status     syscall.WaitStatus
		panic      bool
	}{
		{name: "exit", code: "guest_child_stop_exit", status: 7 << 8},
		{name: "panic", code: "guest_child_stop_panic", status: 1 << 8, panic: true},
		{name: "interrupt", code: "guest_child_stop_sigint", status: syscall.WaitStatus(syscall.SIGINT)},
		{name: "kill", code: "guest_child_stop_sigkill", status: syscall.WaitStatus(syscall.SIGKILL)},
		{name: "other", code: "guest_child_stop_signal", status: syscall.WaitStatus(syscall.SIGTERM)},
	} {
		checkModuleChildStopStatus(t, status.name, status.status, status.panic, status.code)
	}
}

func checkModuleChildStopStatus(t *testing.T, name string, status syscall.WaitStatus, panicMarker bool, code string) {
	t.Helper()
	// Real subprocess wait status comes from the fixture's selected exit behavior.
	path := "/bin/sh"
	script := "exit 7"
	if status == 1<<8 {
		script = "exit 1"
	}
	if status.Signaled() {
		script = "kill -" + strconv.Itoa(int(status.Signal())) + " $$"
	}
	command := exec.CommandContext(t.Context(), path, "-c", script) //nolint:gosec // Closed test-only scripts chosen above, no caller input.
	err := command.Run()
	var wait *exec.ExitError
	if !errors.As(err, &wait) {
		t.Fatal("wait-status fixture did not fail", name, err)
	}
	stopped := engineChildStopError{phase: "wait", cause: err}
	if got := moduleChildStopFailureCode(stopped, panicMarker); got != code || !errors.Is(stopped, err) {
		t.Fatal("closed code or actual wait failure changed", name, got)
	}
}

func TestEngineStopRetainsRealExitClassification(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	sink := &moduleNativeDiagnostics{}
	child, err := localexec.StartForeground(ctx, localexec.ForegroundCommand{Path: os.Args[0], Args: []string{"-test.run=^TestEngineStopChildHelper$"}, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "GORACE=atexit_sleep_ms=0", "BRINE_ENGINE_STOP_READY=" + ready, "BRINE_ENGINE_STOP_OUTCOME=failure"}, Dir: dir, Stderr: sink})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.KillGroup() }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err = os.Stat(ready); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("owned child did not become ready")
		}
	}
	err = stopEngineChild(ctx, child, child.Done(), time.Second)
	var failed *exec.ExitError
	if !errors.As(err, &failed) || failed.ExitCode() != 7 || moduleChildStopFailureCode(err, sink.panicSeen()) != "guest_child_stop_exit" {
		t.Fatal("stop swallowed actual native exit failure", err)
	}
}

func TestEngineStopDiagnosticIgnoreInterruptHelper(t *testing.T) {
	ready := os.Getenv("BRINE_DIAGNOSTIC_IGNORE_READY")
	if ready == "" {
		return
	}
	signal.Ignore(os.Interrupt)
	if err := os.WriteFile(ready, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	} //nolint:gosec // Parent-owned closed fixture readiness path.
	time.Sleep(10 * time.Second)
}

func TestEngineStopLabelsActualBudgetAndCanceledContext(t *testing.T) {
	for _, scenario := range []string{"budget", "context"} {
		t.Run(scenario, func(t *testing.T) { checkEngineStopForcedReason(t, scenario) })
	}
}

func checkEngineStopForcedReason(t *testing.T, scenario string) {
	t.Helper()
	parent, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	child, err := localexec.StartForeground(parent, localexec.ForegroundCommand{Path: os.Args[0], Args: []string{"-test.run=^TestEngineStopDiagnosticIgnoreInterruptHelper$"}, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "GORACE=atexit_sleep_ms=0", "BRINE_DIAGNOSTIC_IGNORE_READY=" + ready}, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.KillGroup() }()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if _, err = os.Stat(ready); err == nil {
			break
		}
		select {
		case <-tick.C:
		case <-parent.Done():
			t.Fatal("owned child never became ready")
		}
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	budget := 10 * time.Millisecond
	if scenario == "context" {
		cancel()
		budget = time.Second
	}
	err = stopEngineChild(ctx, child, child.Done(), budget)
	if !errors.Is(err, errEngineSource) || moduleChildStopFailureCode(err, false) != "guest_child_stop_"+scenario {
		t.Fatal("actual forced-stop source/category changed", scenario, err)
	}
	if err = child.KillGroup(); err == nil {
		t.Fatal("forced-stop consumer still running")
	}
}
