//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"golang.org/x/sys/unix"
)

func TestModuleNativeChildTransitionHelper(t *testing.T) {
	ready := os.Getenv("BRINE_NATIVE_CHILD_READY")
	if ready == "" {
		return
	}
	if err := os.WriteFile(ready, nil, 0600); err != nil { //nolint:gosec // Parent-owned disposable child fixture.
		t.Fatal(err)
	}
	if err := waitModuleChildFixtureFile(t.Context(), os.Getenv("BRINE_NATIVE_CHILD_RELEASE")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Exec(os.Args[0], []string{os.Args[0], "-test.run=^TestModuleNativeChildReadyHelper$"}, os.Environ()); err != nil { //nolint:gosec // Fixed exec transition inside the parent-owned child fixture.
		t.Fatal(err)
	}
}

func TestModuleNativeChildReadyHelper(t *testing.T) {
	if os.Getenv("BRINE_NATIVE_CHILD_READY") == "" {
		return
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	select {
	case <-interrupt:
	case <-time.After(5 * time.Second):
		t.Fatal("child stop was not issued")
	}
}

func waitModuleChildFixtureFile(ctx context.Context, path string) error {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil { //nolint:gosec // Parent-selected readiness file for this disposable child.
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func moduleChildCommandFixture(t *testing.T) (*localexec.ForegroundProcess, *engineNativeHandle, string) {
	t.Helper()
	directory := t.TempDir()
	ready, release := filepath.Join(directory, "ready"), filepath.Join(directory, "release")
	lifetime, cancel := context.WithCancel(t.Context())
	child, err := localexec.StartForeground(lifetime, localexec.ForegroundCommand{Path: os.Args[0], Args: []string{"-test.run=^TestModuleNativeChildTransitionHelper$"}, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + directory, "GORACE=atexit_sleep_ms=0", "BRINE_NATIVE_CHILD_READY=" + ready, "BRINE_NATIVE_CHILD_RELEASE=" + release}, Dir: directory})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-child.Done():
		case <-time.After(5 * time.Second):
			t.Error("native fixture survived teardown")
		}
	})
	wait, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	if err := waitModuleChildFixtureFile(wait, ready); err != nil {
		t.Fatal(err)
	}
	process := "/proc/" + strconv.Itoa(child.PID())
	raw, err := engineProcessFile(process, "stat")
	pid, ticks, parseErr := parseEngineProcessStat(string(raw))
	if err != nil || parseErr != nil {
		t.Fatal("child identity unavailable", err, parseErr)
	}
	group, err := moduleStageChildGroup(process)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.PidfdOpen(child.PID(), unix.PIDFD_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	handle := &engineNativeHandle{pid: pid, ticks: ticks, cgroup: group, fd: fd}
	t.Cleanup(handle.close)
	return child, handle, release
}

func TestModuleChildCommandWaitRetainsIdentityAcrossExec(t *testing.T) {
	child, handle, release := moduleChildCommandFixture(t)
	args := []string{"-test.run=^TestModuleNativeChildReadyHelper$"}
	process := "/proc/" + strconv.Itoa(child.PID())
	if engineProcessCommand(process, os.Args[0], args) {
		t.Fatal("unexecuted command was accepted")
	}
	startup, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- waitEngineProcessCommand(startup, handle, os.Args[0], args, func() bool { return handle.alive() == nil })
	}()
	if err := os.WriteFile(release, nil, 0600); err != nil { //nolint:gosec // Parent-owned disposable child fixture.
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal("same child command transition was refused", err)
	}
	cancel()
	if handle.alive() != nil || !engineProcessCommand(process, os.Args[0], args) {
		t.Fatal("readiness cancellation killed or replaced the child")
	}
	if err := child.Interrupt(); err != nil {
		t.Fatal(err)
	}
}

func TestModuleStageListenerKeepsExpiredStartupDeadline(t *testing.T) {
	child, handle, _ := moduleChildCommandFixture(t)
	startup, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	args := []string{"-test.run=^TestModuleNativeChildTransitionHelper$"}
	if err := waitEngineProcessCommand(startup, handle, os.Args[0], args, func() bool { return handle.alive() == nil }); err != nil {
		t.Fatal(err)
	}
	<-startup.Done()
	started := time.Now()
	_, err := observeBoundModuleStage(t.Context(), startup, handle, moduleStageInput{}, spacetimedb.PublisherCredential{})
	if moduleStageFailureCode(err, "") != "guest_listener" || time.Since(started) > time.Second {
		t.Fatal("listener gained another startup budget", err)
	}
	if handle.alive() != nil {
		t.Fatal("readiness deadline killed the enclosing child lifetime", child.PID())
	}
}

func TestModuleChildCommandWaitRefusesForeignIdentityAndDeadline(t *testing.T) {
	for _, kind := range []string{"path", "arguments", "pid", "ticks", "cgroup", "deadline", "death"} {
		t.Run(kind, func(t *testing.T) { testModuleChildCommandRefusal(t, kind) })
	}
}

func testModuleChildCommandRefusal(t *testing.T, kind string) {
	t.Helper()
	child, handle, _ := moduleChildCommandFixture(t)
	path, args := os.Args[0], []string{"-test.run=^TestModuleNativeChildTransitionHelper$"}
	startup, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	switch kind {
	case "path":
		path += "-foreign"
	case "arguments":
		args = []string{"-test.run=foreign"}
	case "pid":
		handle.pid = uint32(os.Getpid()) //nolint:gosec // The current Linux process PID fits uint32.
	case "ticks":
		handle.ticks++
	case "cgroup":
		handle.cgroup += "-foreign"
	case "deadline":
		cancel()
	case "death":
		if err := child.KillGroup(); err != nil {
			t.Fatal(err)
		}
		if err := waitModuleChildFixtureExit(t.Context(), handle); err != nil {
			t.Fatal(err)
		}
	}
	if err := waitEngineProcessCommand(startup, handle, path, args, func() bool { return handle.alive() == nil }); !errors.Is(err, errEngineSource) {
		t.Fatal("foreign, expired or dead child was accepted", kind, err)
	}
}

func TestEngineCommandWaitRefusesGuardDriftWhileStarting(t *testing.T) {
	_, handle, _ := moduleChildCommandFixture(t)
	startup, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	checks := 0
	guard := func() bool { checks++; return checks < 2 }
	started := time.Now()
	err := waitEngineProcessCommand(startup, handle, os.Args[0], []string{"-test.run=foreign"}, guard)
	if !errors.Is(err, errEngineSource) || checks != 2 || time.Since(started) > time.Second {
		t.Fatal("changed non-command guard waited for another identity", err, checks)
	}
}

func waitModuleChildFixtureExit(ctx context.Context, handle *engineNativeHandle) error {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if handle.exited() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
