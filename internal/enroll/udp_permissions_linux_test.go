//go:build linux

package enroll

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestUDPGrantFenceSharesMutationLockAndRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mutation.lock")
	lock, err := lockUDPGrantState(t.Context(), path, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if second, err := lockUDPGrantState(ctx, path, os.Getuid(), os.Getgid()); err == nil {
		_ = second.Close()
		t.Fatal("operator fence did not share existing mutation lock")
	}
	link := filepath.Join(dir, "linked.lock")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if held, err := lockUDPGrantState(t.Context(), link, os.Getuid(), os.Getgid()); err == nil {
		_ = held.Close()
		t.Fatal("operator lock followed symlink")
	}
}

type grantCheckRunner struct {
	out   localexec.Capture
	err   error
	calls [][]string
}

func (r *grantCheckRunner) CaptureStdout(_ context.Context, limit int, path string, args ...string) (localexec.Capture, error) {
	r.calls = append(r.calls, append([]string{strconv.Itoa(limit), path}, args...))
	return r.out, r.err
}

// The local resolver deliberately searches only /usr/bin and /bin, while
// Debian 13 installs runuser only as /usr/sbin/runuser.
func TestUDPGrantCheckInvokesAbsoluteRunuser(t *testing.T) {
	r := &grantCheckRunner{out: localexec.Capture{Stdout: `{"held":false}`}}
	held, err := udpGrantHeldVia(t.Context(), r, "api")
	want := [][]string{{"4096", "/usr/sbin/runuser", "-u", "brine", "--", "/usr/local/bin/brine", "host", "udp-grant-check", "api"}}
	if err != nil || held || !slices.EqualFunc(r.calls, want, slices.Equal) {
		t.Fatal(held, err, r.calls)
	}
	if path := enrollmentToolPath("runuser"); path != "/usr/sbin/runuser" {
		t.Fatal("UDP grant check and enrollment disagree on runuser", path)
	}
}

func TestUDPGrantCheckRefusesUnprovenAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, app string
		out       localexec.Capture
		err       error
		held      bool
		refused   bool
	}{
		{name: "held", app: "api", out: localexec.Capture{Stdout: `{"held":true}`}, held: true},
		{name: "invalid app", app: "../api", out: localexec.Capture{Stdout: `{"held":false}`}, refused: true},
		{name: "helper failure", app: "api", out: localexec.Capture{Stdout: `{"held":false}`}, err: errors.New("exit 1"), refused: true},
		{name: "overflow", app: "api", out: localexec.Capture{Stdout: `{"held":false}`, Overflow: true}, refused: true},
		{name: "extra field", app: "api", out: localexec.Capture{Stdout: `{"held":false,"x":1}`}, refused: true},
		{name: "wrong type", app: "api", out: localexec.Capture{Stdout: `{"held":"false"}`}, refused: true},
		{name: "empty", app: "api", refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &grantCheckRunner{out: tc.out, err: tc.err}
			held, err := udpGrantHeldVia(t.Context(), r, tc.app)
			if tc.refused != (err != nil) || held != tc.held {
				t.Fatal(held, err)
			}
			if tc.app != "api" && len(r.calls) != 0 {
				t.Fatal("invalid app reached the helper", r.calls)
			}
		})
	}
}
