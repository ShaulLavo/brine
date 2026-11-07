//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"golang.org/x/sys/unix"
)

func gateDirectory(t *testing.T) *admission.Directory {
	t.Helper()
	original := os.Geteuid()
	if err := syscall.Seteuid(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Seteuid(original); err != nil {
			t.Error(err)
		}
	})
	// The administrative tests create root-owned stores below the synthetic
	// root-owned temp ancestor, not below a bound user-owned /work ancestor.
	t.Setenv("TMPDIR", "/tmp")
	t.Setenv("GOTMPDIR", "/tmp")
	directory, err := admission.OpenDirectory(admissionRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := directory.Close(); err != nil {
			t.Error(err)
		}
		if err := writeAdmissionFixture([]byte(`{"version":1,"open":true}`)); err != nil {
			t.Error(err)
		}
	})
	return directory
}

func gatePending() admission.Pending {
	return admission.Pending{Kind: admission.Update, ID: strings.Repeat("a", 32), Stage: admission.Admitting, Quiescence: admission.Store, Since: "2026-11-07T00:00:00Z"}
}

func gateDrain(t *testing.T, directory *admission.Directory) *admission.Drain {
	t.Helper()
	drain, err := directory.CloseAdmission(t.Context(), gatePending())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := drain.Close(); err != nil {
			t.Error(err)
		}
	})
	return drain
}

func gateStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil { // #nosec G302 -- Private state directory, not a file.
		t.Fatal(err)
	}
	return dir
}

func TestAdmissionClosedRefusesEveryStoreMode(t *testing.T) {
	directory := gateDirectory(t)
	drain := gateDrain(t, directory)
	dir := gateStateDir(t)
	assertClosedGateModes(t, dir)
	if err := drain.Close(); err != nil {
		t.Fatal(err)
	}
	assertClosedGateModes(t, dir)
}

func assertClosedGateModes(t *testing.T, dir string) {
	t.Helper()
	for _, mode := range []stateMode{stateMutating, stateReadOnly, stateSnapshot, statePreview} {
		state, err := openState(t.Context(), dir, mode, nil)
		if state != nil || !errors.Is(err, admission.ErrClosed) {
			t.Fatalf("mode %d: %v %v", mode, state, err)
		}
	}
	assertEmptyGateState(t, dir)
}

func TestAdmissionUnavailableDoesNotInitialize(t *testing.T) {
	for _, raw := range []string{"", `{"version":1,"open":true,"unknown":true}`} {
		testUnavailableGate(t, raw)
	}
}

func testUnavailableGate(t *testing.T, raw string) {
	t.Helper()
	directory := gateDirectory(t)
	_ = directory
	if err := os.WriteFile(filepath.Join(admissionRoot, "state.json"), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	} // #nosec G306 -- Protected namespace fixture.
	dir := gateStateDir(t)
	state, err := openState(t.Context(), dir, stateMutating, nil)
	if state != nil || !errors.Is(err, admission.ErrUnavailable) {
		t.Fatal(state, err)
	}
	assertEmptyGateState(t, dir)
}

func TestAdmissionMissingStateAndLockFailClosed(t *testing.T) {
	for _, name := range []string{"state.json", "lock"} {
		testMissingGateArtifact(t, name)
	}
}

func testMissingGateArtifact(t *testing.T, name string) {
	t.Helper()
	gateDirectory(t)
	path := filepath.Join(admissionRoot, name)
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Rename(path+".saved", path); err != nil {
			t.Error(err)
		}
	}()
	dir := gateStateDir(t)
	state, err := openState(t.Context(), dir, stateMutating, nil)
	if state != nil || !errors.Is(err, admission.ErrUnavailable) {
		t.Fatal(state, err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("gate recreated missing artifact", err)
	}
	assertEmptyGateState(t, dir)
}

func TestAdmissionStoreLifetimeBlocksDrainThroughCancellation(t *testing.T) {
	directory := gateDirectory(t)
	ctx, cancel := context.WithCancel(t.Context())
	state, err := openState(ctx, gateStateDir(t), stateMutating, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	cancel()
	assertGateLock(t, "lock", true)
	err = cancelHeldGateOperation(t, directory, "lock", func(ctx context.Context) error { _, err := directory.CloseAdmission(ctx, gatePending()); return err })
	var drain *admission.Drain

	if drain != nil || !errors.Is(err, admission.ErrDrainTimeout) {
		t.Fatal(drain, err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	assertGateLock(t, "lock", false)
	gateDrain(t, directory)
}

func assertGateLock(t *testing.T, name string, held bool) {
	t.Helper()
	fd, err := unix.Open(filepath.Join(admissionRoot, name), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	if held && !errors.Is(err, unix.EWOULDBLOCK) || !held && err != nil {
		t.Fatalf("%s held=%v: %v", name, held, err)
	}
}

func assertEmptyGateState(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("state files created", entries, err)
	}
}

func TestAdmissionInventoryEmptyAndClosed(t *testing.T) {
	directory := gateDirectory(t)
	dir := gateStateDir(t)
	collector := inventoryCollector(make([]byte, 32), dir)
	value, err := collector.StateInventory(t.Context())
	if err != nil || value.Generation != 0 || value.Apps == nil || len(value.Apps) != 0 {
		t.Fatal(value, err)
	}
	assertEmptyGateState(t, dir)
	gateDrain(t, directory)
	if _, err := collector.StateInventory(t.Context()); !errors.Is(err, admission.ErrClosed) {
		t.Fatal(err)
	}
	assertEmptyGateState(t, dir)
}

func TestAdmissionUDPInitialStateRemainsExact(t *testing.T) {
	directory := gateDirectory(t)
	dir := gateStateDir(t)
	held, err := readUDPGrantHeld(t.Context(), dir, "hello")
	if err != nil || held {
		t.Fatal(held, err)
	}
	assertEmptyGateState(t, dir)
	lock := filepath.Join(dir, "mutation.lock")
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if held, err = readUDPGrantHeld(t.Context(), dir, "hello"); err != nil || held {
		t.Fatal(held, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unknown"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readUDPGrantHeld(t.Context(), dir, "hello"); err == nil {
		t.Fatal("unknown initial contents admitted")
	}
	gateDrain(t, directory)
	if _, err = readUDPGrantHeld(t.Context(), dir, "hello"); !errors.Is(err, admission.ErrClosed) {
		t.Fatal(err)
	}
}

func TestAdmissionClosedMetadataAndDiagnosticComposition(t *testing.T) {
	directory := gateDirectory(t)
	gateDrain(t, directory)
	dir := gateStateDir(t)
	if _, err := (metadataStatus{dir: dir}).Operation(t.Context(), strings.Repeat("b", 32), 0); !errors.Is(err, admission.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := (nativeBackupStatusReader{stateDir: dir}).BackupStatus(t.Context(), dispatch.NativeBackupStatusArgs{App: "example", Database: "main"}); !errors.Is(err, admission.ErrClosed) {
		t.Fatal(err)
	}
	diagnostic, _ := diagnosticReaders(dir, nil)
	reader := diagnostic.(diagnose.Reader)
	if _, err := reader.Store.AppNames(t.Context()); !errors.Is(err, admission.ErrClosed) {
		t.Fatal(err)
	}
	value, err := (readinessControl{dir: dir}).Control(t.Context())
	if err != nil || value.State != "preview_unavailable" || value.QuickCheck.Value != nil {
		t.Fatal(value, err)
	}
	assertEmptyGateState(t, dir)
}

func TestAdmissionExemptPausedBeforeOpenBlocksRevokeAndAbort(t *testing.T) {
	directory := gateDirectory(t)
	drain := gateDrain(t, directory)
	selector, err := drain.EnableStage(t.Context(), admission.Migrating)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := acquireStateLease(t.Context(), &selector)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	assertStageDrainBlocked(t, directory, drain)
	if denied, err := acquireStateLease(t.Context(), &selector); denied != nil || !errors.Is(err, admission.ErrClosed) {
		t.Fatal(denied, err)
	}
	// This admitted child was paused before SQLite open. Its lease, not an fd
	// snapshot, prevents abort from racing its eventual open.
	state, err := openLeasedState(t.Context(), gateStateDir(t), stateMutating, lease)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	assertStageDrainBlocked(t, directory, drain)
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	if err := drain.SetStage(t.Context(), admission.Fenced); err != nil {
		t.Fatal(err)
	}
	next, err := drain.EnableStage(t.Context(), admission.Migrating)
	if err != nil || next.Attempt == selector.Attempt {
		t.Fatal(next, err)
	}
	if denied, err := acquireStateLease(t.Context(), &selector); denied != nil || !errors.Is(err, admission.ErrClosed) {
		t.Fatal(denied, err)
	}
	state, err = openState(t.Context(), gateStateDir(t), stateMutating, &next)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertStageDrainBlocked(t *testing.T, directory *admission.Directory, drain *admission.Drain) {
	t.Helper()
	before, err := directory.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = cancelHeldGateOperation(t, directory, "stage.lock", drain.RevokeAndDrain)
	if !errors.Is(err, admission.ErrDrainTimeout) {
		t.Fatal(err)
	}
	err = cancelHeldGateOperation(t, directory, "stage.lock", func(ctx context.Context) error { return drain.SetStage(ctx, admission.Terminal) })
	if !errors.Is(err, admission.ErrDrainTimeout) {
		t.Fatal("abort passed undrained stage", err)
	}
	after, err := directory.Read(t.Context())
	if err != nil || after.Open || after.Pending.Stage != before.Pending.Stage || after.Pending.ExemptOpen {
		t.Fatal("stage changed before admitted child closed", after, err)
	}
}

func cancelHeldGateOperation(t *testing.T, directory *admission.Directory, name string, operation func(context.Context) error) error {
	t.Helper()
	assertGateLock(t, name, true)
	before, err := os.Stat(filepath.Join(admissionRoot, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan struct{})
	result := make(chan error, 1)
	go func() { defer close(finished); result <- operation(ctx) }()
	defer func() { cancel(); <-finished }()
	waitGatePublication(t, directory, before, name)
	assertGateLock(t, name, true)
	cancel()
	return <-result
}

func waitGatePublication(t *testing.T, directory *admission.Directory, before os.FileInfo, name string) {
	t.Helper()
	outer, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if gatePublicationObserved(t, directory, before, name) {
			return
		}
		select {
		case <-outer.Done():
			t.Fatal("hung admission fixture publication", outer.Err())
		case <-ticker.C:
		}
	}
}

func gatePublicationObserved(t *testing.T, directory *admission.Directory, before os.FileInfo, name string) bool {
	t.Helper()
	current, err := os.Stat(filepath.Join(admissionRoot, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, current) {
		return false
	}
	state, err := directory.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return !state.Open && (name != "stage.lock" || !state.Pending.ExemptOpen)
}

func TestAdmissionLostStageReplyStillDrainsStore(t *testing.T) {
	directory := gateDirectory(t)
	drain := gateDrain(t, directory)
	selector, err := drain.EnableStage(t.Context(), admission.Migrating)
	if err != nil {
		t.Fatal(err)
	}
	state, err := openState(t.Context(), gateStateDir(t), stateMutating, &selector)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	if err := drain.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := directory.ResumeClosed(t.Context(), selector.Kind, selector.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resumed.Close() }()
	assertStageDrainBlocked(t, directory, resumed)
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	if err := resumed.SetStage(t.Context(), admission.Terminal); err != nil {
		t.Fatal(err)
	}
	if err := resumed.Reopen(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionStoreLeaseCloseOnExec(t *testing.T) {
	gateDirectory(t)
	for _, mode := range []string{"ordinary", "stage"} {
		runGateExecProbe(t, mode)
	}
}

func runGateExecProbe(t *testing.T, mode string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAdmissionExecProbe$") // #nosec G204 G702 -- Fixed self-test subprocess.
	command.Env = append(os.Environ(), "BRINE_GATE_EXEC="+mode, "BRINE_GATE_STATE="+gateStateDir(t))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %s: %v", mode, output, err)
	}
}

func TestAdmissionExecProbe(t *testing.T) {
	mode := os.Getenv("BRINE_GATE_EXEC")
	if mode == "" {
		return
	}
	if mode == "after" {
		assertGateLock(t, "lock", false)
		assertGateLock(t, "stage.lock", false)
		return
	}
	directory := gateDirectory(t)
	var selector *admission.Exemption
	if mode == "stage" {
		value := gateExecSelector(t, directory)
		selector = &value
	}
	state, err := openState(t.Context(), os.Getenv("BRINE_GATE_STATE"), stateMutating, selector)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	if err := os.Setenv("BRINE_GATE_EXEC", "after"); err != nil {
		t.Fatal(err)
	}
	if err := unix.Exec(os.Args[0], []string{os.Args[0], "-test.run=^TestAdmissionExecProbe$"}, os.Environ()); err != nil {
		t.Fatal(err)
	}
}

func gateExecSelector(t *testing.T, directory *admission.Directory) admission.Exemption {
	t.Helper()
	drain := gateDrain(t, directory)
	selector, err := drain.EnableStage(t.Context(), admission.Migrating)
	if err != nil {
		t.Fatal(err)
	}
	if err := drain.Close(); err != nil {
		t.Fatal(err)
	}
	return selector
}

func TestAdmissionEngineSupervisorReleasesAfterProof(t *testing.T) {
	directory := gateDirectory(t)
	runtime, binding := engineRefusalFixture(t)
	if err := runtime.state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := openState(t.Context(), runtime.stateRoot, stateReadOnly, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime.state, runtime.closeState = state.Store, state.Close
	assertGateLock(t, "lock", true)
	if err := runtime.releaseStartupState(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	if runtime.state != nil {
		t.Fatal("supervisor retained SQLite")
	}
	drain := gateDrain(t, directory)
	runtime.recordEngineRefusal(t.Context(), binding, engineRefusal(engineRefusalNativeExit, errors.New("fixture")))
	if _, err := os.Stat(refusalFile(runtime.stateRoot, binding)); err != nil {
		t.Fatal("lost post-start refusal receipt", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := drain.Close(); err != nil {
		t.Fatal(err)
	}
}
