package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestTUIWithRedirectedStdin(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "brine")
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), time.Minute)
	defer cancelBuild()
	if output, err := exec.CommandContext(buildCtx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}

	for _, interrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("interrupt=%t", interrupt), func(t *testing.T) { testTUIProcess(t, binary, interrupt) })
	}
}

func testTUIProcess(t *testing.T, binary string, interrupt bool) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "pty-master")
	defer master.Close()
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
	defer slave.Close()
	if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80}); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "tui")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, slave, slave
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 1}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	}()
	if err := slave.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	queries := []struct {
		request, response string
		answered          int
	}{
		{"\x1b]10;?\x1b\\", "\x1b]10;rgb:ffff/ffff/ffff\x1b\\", 0},
		{"\x1b]11;?\x1b\\", "\x1b]11;rgb:0000/0000/0000\x1b\\", 0},
		{"\x1b[6n", "\x1b[1;1R", 0},
	}
	quitSent := false
	for ctx.Err() == nil {
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(poll, 100); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			t.Fatal(err)
		}
		if poll[0].Revents == 0 {
			continue
		}
		buf := make([]byte, 4096)
		n, err := unix.Read(fd, buf)
		if errors.Is(err, unix.EIO) || n == 0 {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		output.Write(buf[:n])
		for i := range queries {
			query := &queries[i]
			for count := strings.Count(output.String(), query.request); query.answered < count; query.answered++ {
				if _, err := master.WriteString(query.response); err != nil {
					t.Fatal(err)
				}
			}
		}
		if !quitSent && strings.Contains(output.String(), "Press q to quit.") {
			if interrupt {
				if err := cmd.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
			} else if _, err := master.WriteString("q"); err != nil {
				t.Fatal(err)
			}
			quitSent = true
		}
	}
	err = cmd.Wait()
	if interrupt {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 130 {
			t.Fatalf("SIGINT exit: %v; want 130", err)
		}
	} else if err != nil {
		t.Fatalf("tui exit: %v\nterminal output: %q", err, output.String())
	}
	if !quitSent || !strings.Contains(output.String(), "A foundation, not yet a deployment engine.") {
		t.Fatalf("welcome screen was not displayed: %q", output.String())
	}
}
