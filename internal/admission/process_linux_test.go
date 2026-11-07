//go:build linux

package admission

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func openProcessFixture(t *testing.T, path string) *Directory {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(fd), path)
	d, err := checkedDirectory(f, fixtureOwner(t, f))
	if err != nil {
		t.Fatal(err)
	}
	deferOnCleanup(t, d)
	return d
}

func deferOnCleanup(t *testing.T, c interface{ Close() error }) {
	t.Helper()
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
}

func TestLeaseProcess(t *testing.T) {
	mode := os.Getenv("BRINE_ADMISSION_TEST_MODE")
	if mode == "" {
		return
	}
	d := openProcessFixture(t, os.Getenv("BRINE_ADMISSION_TEST_PATH"))
	ctx := deadline(t, 10*time.Second)
	switch mode {
	case "shared":
		lease, err := d.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		deferOnCleanup(t, lease)
		if _, err = os.Stdout.WriteString("ready\n"); err != nil {
			t.Fatal(err)
		}
		var b [1]byte
		if _, err = os.Stdin.Read(b[:]); err != nil {
			t.Fatal(err)
		}
	case "closed-exit":
		r, err := d.CloseAdmission(ctx, pendingFixture())
		if err != nil {
			t.Fatal(err)
		}
		// Deliberately omit r.Close: process exit must release the fd, not the state.
		_ = r
	case "exec", "stage-exec":
		lease := execLease(t, d, mode)
		deferOnCleanup(t, lease)
		for _, file := range []*os.File{d.dir, lease.file} {
			assertCloseOnExec(t, file)
		}
		t.Setenv("BRINE_ADMISSION_TEST_MODE", "after-exec")
		env := os.Environ()
		if err := unix.Exec(os.Args[0], []string{os.Args[0], "-test.run=^TestLeaseProcess$"}, env); err != nil {
			t.Fatal(err)
		}
	case "after-exec":
		assertExecDrained(t, d, ctx)
	default:
		t.Fatalf("unknown process mode %s", mode)
	}
}

func execLease(t *testing.T, d *Directory, mode string) *Lease {
	t.Helper()
	if mode == "exec" {
		lease, err := d.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return lease
	}
	r := closeAdmission(t, d)
	e, err := r.EnableStage(context.Background(), Migrating)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, r)
	lease, err := d.AcquireExempt(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func assertCloseOnExec(t *testing.T, f *os.File) {
	t.Helper()
	flags, err := unix.FcntlInt(f.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("close-on-exec: %d %v", flags, err)
	}
}

func processCommand(t *testing.T, path, mode string) *exec.Cmd {
	t.Helper()
	// #nosec G204 G702 -- Re-exec only this test binary with a fixed helper selection.
	cmd := exec.CommandContext(deadline(t, 15*time.Second), os.Args[0], "-test.run=^TestLeaseProcess$")
	cmd.Env = append(os.Environ(), "BRINE_ADMISSION_TEST_PATH="+path, "BRINE_ADMISSION_TEST_MODE="+mode)
	return cmd
}

func TestSharedLeaseAcrossProcesses(t *testing.T) {
	d, path := fixture(t)
	cmd := processCommand(t, path, "shared")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() { reapFailedHelper(cmd, waited) })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatalf("no child readiness: %v", scanner.Err())
	}
	err = cancelAfterState(t, d, func(ctx context.Context) error { _, err := d.CloseAdmission(ctx, pendingFixture()); return err }, func(s State) bool { return !s.Open })
	if !errors.Is(err, ErrDrainTimeout) {
		t.Fatal(err)
	}
	assertOpen(t, d, true)
	if _, err = stdin.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	// Drain stdout before Wait, including the test runner's PASS line.
	for scanner.Scan() {
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatal(err)
	}
	r := closeAdmission(t, d)
	mustClose(t, r)
}

func TestClosedStateSurvivesProcessExit(t *testing.T) {
	d, path := fixture(t)
	output, err := processCommand(t, path, "closed-exit").CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v", output, err)
	}
	s := assertOpen(t, d, false)
	if lease, err := d.Acquire(context.Background()); !errors.Is(err, ErrClosed) {
		if lease != nil {
			mustClose(t, lease)
		}
		t.Fatal(err)
	}
	r, err := d.ResumeClosed(context.Background(), s.Pending.Kind, s.Pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, r)
	assertOpen(t, d, false)
}

func TestLeasesNotInheritedAcrossExec(t *testing.T) {
	for _, mode := range []string{"exec", "stage-exec"} {
		t.Run(mode, func(t *testing.T) {
			assertExecMode(t, mode)
		})
	}
}

func TestLeaseDescriptorLivesUntilClose(t *testing.T) {
	d, path := fixture(t)
	lease, err := d.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fd := lease.file.Fd()
	if _, err = os.Stat(filepath.Join("/proc/self/fd", strconv.FormatUint(uint64(fd), 10))); err != nil {
		t.Fatal(err)
	}
	mustClose(t, lease)
	if _, err = unix.FcntlInt(fd, unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatalf("descriptor retained: %v at %s", err, path)
	}
	mustClose(t, lease)
}

func assertExecDrained(t *testing.T, d *Directory, ctx context.Context) {
	t.Helper()
	// A leaked shared lease from the same process would block an exclusive lock.
	for _, name := range []string{"lock", "stage.lock"} {
		lease, err := d.drain(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		mustClose(t, lease)
	}
}

func assertExecMode(t *testing.T, mode string) {
	t.Helper()
	_, path := fixture(t)
	output, err := processCommand(t, path, mode).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v", output, err)
	}
}

func reapFailedHelper(cmd *exec.Cmd, waited bool) {
	if waited {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
