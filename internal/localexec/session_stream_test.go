package localexec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSessionStreamChild(t *testing.T) {
	if !slices.Contains(os.Args, "BRINE_SESSION_STREAM_CHILD=1") {
		return
	}
	if _, err := os.Stdout.Write(bytes.Repeat([]byte("x"), CommandOutputLimit+1)); err != nil {
		os.Exit(7)
	}
	os.Exit(0)
}

func TestSessionStreamsRealChildBeyondCapturedLimit(t *testing.T) {
	session, err := NewSession(ExecRunner{}, 1234, t.TempDir(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	args := []string{"-test.run=^TestSessionStreamChild$", "--", "BRINE_SESSION_STREAM_CHILD=1"}
	if err := session.RunStreams(context.Background(), os.Args[0], args, nil, &output, SessionStreamOutputLimit, false); err != nil {
		t.Fatal(err)
	}
	if output.Len() != CommandOutputLimit+1 {
		t.Fatal("real streaming child was truncated", output.Len())
	}
}

type sessionStreamFake struct {
	command Command
	calls   int
	run     func(context.Context, io.Reader, io.Writer) error
}

type sessionCapturedOnly struct{ calls int }

func (f *sessionCapturedOnly) Execute(context.Context, Command) (Result, error) {
	f.calls++
	return Result{}, nil
}

func (f *sessionStreamFake) Execute(context.Context, Command) (Result, error) {
	return Result{}, errors.New("captured execution must not replace streams")
}

func (f *sessionStreamFake) RunStreams(ctx context.Context, command Command, input io.Reader, output io.Writer) error {
	f.calls++
	f.command = command
	return f.run(ctx, input, output)
}

func TestSessionStreamsBeyondCapturedLimitWithDetachedCommand(t *testing.T) {
	want := bytes.Repeat([]byte("x"), CommandOutputLimit+1)
	fake := &sessionStreamFake{run: func(_ context.Context, input io.Reader, output io.Writer) error {
		raw, err := io.ReadAll(input)
		if err != nil || string(raw) != "fixed request" {
			return errors.New("wrong request")
		}
		_, err = output.Write(want)
		return err
	}}
	session, err := NewSession(fake, 1234, t.TempDir(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "fixed-image"}
	var output bytes.Buffer
	if err := session.RunStreams(context.Background(), "/usr/bin/podman", args, strings.NewReader("fixed request"), &output, SessionStreamOutputLimit, true); err != nil {
		t.Fatal(err)
	}
	args[0] = "changed"
	if !bytes.Equal(output.Bytes(), want) || fake.command.Args[0] != "run" || fake.command.Dir != session.dir || fake.command.Timeout != time.Second {
		t.Fatal("session lost bytes or aliased command")
	}
	if !validStreamEnvironment(fake.command.Env) {
		t.Fatal("session supplied duplicate environment keys")
	}
	if !containsEnv(fake.command.Env, "XDG_RUNTIME_DIR=/run/user/1234") || !containsEnv(fake.command.Env, "LC_ALL=C") {
		t.Fatal("enrolled session overrides changed")
	}
	fake.command.Env[0] = "FAKE=changed"
	if containsEnv(session.env, "FAKE=changed") {
		t.Fatal("executor mutated session environment")
	}
}

func containsEnv(environment []string, expected string) bool {
	for _, entry := range environment {
		if entry == expected {
			return true
		}
	}
	return false
}

func TestSessionStreamsRefuseUnsupportedExecutorAndQuotaBypass(t *testing.T) {
	captured := &sessionCapturedOnly{}
	unsupported, err := NewSession(captured, 1234, t.TempDir(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := unsupported.RunStreams(context.Background(), "/usr/bin/podman", nil, nil, &output, 32, false); err == nil {
		t.Fatal("nonstreaming executor silently admitted")
	}
	if captured.calls != 0 {
		t.Fatal("streaming refusal invoked captured executor")
	}
	fake := &sessionStreamFake{run: func(_ context.Context, _ io.Reader, output io.Writer) error {
		_, _ = output.Write(bytes.Repeat([]byte("x"), 33))
		return nil
	}}
	session, err := NewSession(fake, 1234, t.TempDir(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.RunStreams(context.Background(), "/usr/bin/podman", nil, nil, &output, 32, true); !errors.Is(err, ErrOutputLimit) {
		t.Fatal("ignored writer overflow became success", err)
	}
	if output.Len() != 0 {
		t.Fatal("over-quota chunk reached caller")
	}
	for _, limit := range []int{0, -1, SessionStreamOutputLimit + 1} {
		if err := session.RunStreams(context.Background(), "/usr/bin/podman", nil, nil, &output, limit, false); err == nil {
			t.Fatal("invalid caller quota admitted", limit)
		}
	}
	if fake.calls != 1 {
		t.Fatal("invalid quota launched executor")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = session.RunStreams(ctx, "/usr/bin/podman", nil, nil, &output, 32, true)
	var classified *Error
	if !errors.As(err, &classified) || classified.Kind != Timeout || fake.calls != 1 {
		t.Fatal("already canceled call acquired unknown effect", err)
	}
}

func TestSessionStreamsUseEarlierDeadlineAndSanitizeErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	fake := &sessionStreamFake{run: func(run context.Context, _ io.Reader, _ io.Writer) error {
		actual, ok := run.Deadline()
		if !ok || actual != deadline {
			return errors.New("deadline widened")
		}
		<-run.Done()
		return nil
	}}
	session, err := NewSession(fake, 1234, t.TempDir(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = session.RunStreams(ctx, "/usr/bin/podman", nil, nil, &output, 32, true)
	var classified *Error
	if !errors.As(err, &classified) || classified.Kind != UnknownOutcome {
		t.Fatal("expired stream became success", err)
	}
	fake.run = func(context.Context, io.Reader, io.Writer) error { return errors.New("fake-private-value") }
	err = session.RunStreams(context.Background(), "/usr/bin/podman", nil, nil, &output, 32, false)
	if !errors.As(err, &classified) || classified.Kind != Failed || strings.Contains(err.Error(), "fake-private-value") {
		t.Fatal("executor error was not sanitized", err)
	}
}
