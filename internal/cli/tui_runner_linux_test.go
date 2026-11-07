package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"testing"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/sys/unix"
)

// openTestPTY returns a pseudo-terminal pair sized 80x24. The test process
// does not take it as its controlling terminal.
func openTestPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Skip("PTY unavailable", err)
	}
	master := os.NewFile(uintptr(fd), "pty-master")
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80}); err != nil {
		t.Fatal(err)
	}
	return master, slave
}

// collect copies terminal output until the master is closed or hung up.
func collect(master *os.File) *lockedBuffer {
	out := &lockedBuffer{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			_, _ = out.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}()
	return out
}

func noFallback(t *testing.T) func() (*os.File, error) {
	return func() (*os.File, error) {
		t.Error("terminal stdin was replaced by the controlling terminal")
		return nil, errors.New("unexpected open")
	}
}

// A single "q" without a newline reaches the program only in raw mode, so the
// wrapped descriptor must still reach Bubble Tea's raw-mode setup.
func TestRunTUITerminalStdinKeepsRawModeAndDescriptor(t *testing.T) {
	master, slave := openTestPTY(t)
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	out := collect(master)
	terminal := tuiTerminal{stdin: slave, openTTY: noFallback(t)}
	done := runSession(context.Background(), fixtureHost(), func(ctx context.Context, m tea.Model) (tea.Model, error) {
		return terminal.run(ctx, m, nil, slave)
	})
	waitForOutput(t, out, "fixture-jobs")
	if _, err := master.WriteString("q"); err != nil {
		t.Fatal(err)
	}
	if outcome := await(t, done); outcome.err != nil {
		t.Fatalf("quit returned %v", outcome.err)
	}
	after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if *after != *before {
		t.Fatal("terminal mode was not restored")
	}
	if _, err := slave.Write([]byte("still open\r\n")); err != nil {
		t.Fatalf("runner closed the borrowed terminal: %v", err)
	}
}

func TestRunTUITerminalHangupEndsSession(t *testing.T) {
	master, slave := openTestPTY(t)
	collect(master)
	host := newBlockingHost()
	terminal := tuiTerminal{stdin: slave, openTTY: noFallback(t)}
	done := runSession(context.Background(), host, func(ctx context.Context, m tea.Model) (tea.Model, error) {
		return terminal.run(ctx, m, nil, slave)
	})
	receive(t, host.started, "apps")
	if err := master.Close(); err != nil {
		t.Fatal(err)
	}
	// A hung-up terminal reads as end of file or EIO depending on timing.
	err := await(t, done).err
	cause := error(io.EOF)
	if errors.Is(err, unix.EIO) {
		cause = unix.EIO
	}
	requireIOFailure(t, err, cause)
	receive(t, host.canceled, "apps")
}

type wrappedTUITerminal struct{ *os.File }

func TestRunTUIWrappedInputKeepsRawMode(t *testing.T) {
	master, slave := openTestPTY(t)
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	out := collect(master)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runSession(ctx, fixtureHost(), func(ctx context.Context, m tea.Model) (tea.Model, error) {
		return RunTUI(ctx, m, wrappedTUITerminal{slave}, slave)
	})
	waitForOutput(t, out, "fixture-jobs")
	during, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	cooked := during.Lflag&unix.ICANON != 0
	if _, err := master.WriteString("q"); err != nil {
		t.Fatal(err)
	}
	if cooked {
		cancel()
	}
	outcome := await(t, done)
	if cooked {
		t.Fatalf("wrapped input lost raw mode: %v", outcome.err)
	}
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if *after != *before {
		t.Fatal("wrapped input terminal was not restored")
	}
	if _, err := slave.WriteString("still borrowed\r\n"); err != nil {
		t.Fatal(err)
	}
}

func TestRunTUIWrappedOutputKeepsWindowSize(t *testing.T) {
	master, slave := openTestPTY(t)
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 30, Col: 100}); err != nil {
		t.Fatal(err)
	}
	out := collect(master)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runSession(ctx, fixtureHost(), func(ctx context.Context, m tea.Model) (tea.Model, error) {
		return RunTUI(ctx, m, slave, wrappedTUITerminal{slave})
	})
	waitForOutput(t, out, "fixture-jobs")
	if _, err := master.WriteString("q"); err != nil {
		t.Fatal(err)
	}
	outcome := await(t, done)
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	model := outcome.final.(tuiModel)
	if model.width != 100 || model.height != 30 {
		t.Fatalf("wrapped output lost terminal size: %dx%d", model.width, model.height)
	}
	if _, err := slave.WriteString("still borrowed\r\n"); err != nil {
		t.Fatal(err)
	}
}
