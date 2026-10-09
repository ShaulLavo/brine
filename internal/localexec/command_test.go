package localexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCommandHelper(t *testing.T) {
	if !slices.Contains(os.Args, "BRINE_COMMAND_HELPER=1") {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "input":
		b, _ := io.ReadAll(os.Stdin)
		wd, _ := os.Getwd()
		os.Stdout.WriteString(string(b) + "|" + os.Getenv("XDG_RUNTIME_DIR") + "|" + wd)
		os.Stderr.WriteString("diagnostic")
	case "large":
		os.Stdout.WriteString(strings.Repeat("x", CommandOutputLimit+1))
		os.Stderr.WriteString(strings.Repeat("y", CommandOutputLimit+1))
	case "sleep":
		time.Sleep(30 * time.Second)
	case "fail":
		os.Stderr.WriteString("private-secret")
		os.Exit(7)
	}
	os.Exit(0)
}

func TestExecuteInputEnvironmentDirectory(t *testing.T) {
	t.Setenv("BRINE_COMMAND_HELPER", "1")
	dir := t.TempDir()
	result, err := (ExecRunner{}).Execute(context.Background(), Command{Path: os.Args[0], Args: []string{"-test.run=^TestCommandHelper$", "--", "BRINE_COMMAND_HELPER=1", "input"}, Stdin: []byte("secret"), Env: []string{"XDG_RUNTIME_DIR=/run/user/1234"}, Dir: dir, Timeout: 3 * time.Second})
	wantStderr := "diagnostic"
	if testing.CoverMode() != "" {
		wantStderr += "warning: GOCOVERDIR not set, no coverage data emitted\n"
	}
	if err != nil || result.Stdout != "secret|/run/user/1234|"+dir || result.Stderr != wantStderr {
		t.Fatalf("unexpected execution result: %#v %v", result, err)
	}
}
func TestExecuteBoundsBothStreams(t *testing.T) {
	t.Setenv("BRINE_COMMAND_HELPER", "1")
	result, err := (ExecRunner{}).Execute(context.Background(), Command{Path: os.Args[0], Args: []string{"-test.run=^TestCommandHelper$", "--", "BRINE_COMMAND_HELPER=1", "large"}, Timeout: 3 * time.Second})
	if err != nil || len(result.Stdout) != CommandOutputLimit || len(result.Stderr) != CommandOutputLimit || !result.Truncated {
		t.Fatalf("bounds not enforced: %v", err)
	}
}
func TestExecuteClassification(t *testing.T) {
	t.Setenv("BRINE_COMMAND_HELPER", "1")
	for _, tt := range []struct {
		mode     string
		mutation bool
		want     ErrorKind
	}{{"fail", false, Failed}, {"sleep", false, Timeout}, {"sleep", true, UnknownOutcome}} {
		timeout := 100 * time.Millisecond
		if tt.mode == "fail" {
			timeout = 3 * time.Second
		}
		_, err := (ExecRunner{}).Execute(context.Background(), Command{Path: os.Args[0], Args: []string{"-test.run=^TestCommandHelper$", "--", "BRINE_COMMAND_HELPER=1", tt.mode}, Timeout: timeout, Mutation: tt.mutation})
		var e *Error
		if !errors.As(err, &e) || e.Kind != tt.want || strings.Contains(err.Error(), "private-secret") {
			t.Fatalf("error = %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (ExecRunner{}).Execute(ctx, Command{Path: os.Args[0], Mutation: true, Timeout: time.Second})
	var e *Error
	if !errors.As(err, &e) || e.Kind != Timeout {
		t.Fatalf("prestart = %v", err)
	}
}

func TestSessionValidationAndTruncation(t *testing.T) {
	for _, tt := range []struct {
		dir     string
		timeout time.Duration
	}{{"relative", time.Second}, {"/tmp", 0}} {
		if _, e := NewSession(ExecRunner{}, 1234, tt.dir, tt.timeout); e == nil {
			t.Fatal("invalid session accepted")
		}
	}
	if _, e := NewSession(nil, 1234, "/tmp", time.Second); e == nil {
		t.Fatal("nil executor accepted")
	}
	t.Setenv("BRINE_COMMAND_HELPER", "1")
	s, e := NewSession(helperExecutor{}, 1234, t.TempDir(), 3*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	for _, mutation := range []bool{false, true} {
		_, e := s.Execute(context.Background(), os.Args[0], []string{"-test.run=^TestCommandHelper$", "--", "BRINE_COMMAND_HELPER=1", "large"}, nil, mutation)
		var re *Error
		want := Failed
		if mutation {
			want = UnknownOutcome
		}
		if !errors.As(e, &re) || re.Kind != want {
			t.Fatal(e)
		}
	}
	_, e = s.Execute(context.Background(), "/missing-brine-executable", nil, nil, true)
	var re *Error
	if !errors.As(e, &re) || re.Kind != Failed || re.ExitCode != -1 {
		t.Fatal(e)
	}
}

type errorExecutor struct{ err error }

func (e errorExecutor) Execute(context.Context, Command) (Result, error) { return Result{}, e.err }
func TestSessionDoesNotLeakExecutorErrors(t *testing.T) {
	for _, err := range []error{errors.New("private-secret"), fmt.Errorf("private-secret: %w", &Error{Kind: Failed}), context.DeadlineExceeded} {
		s, e := NewSession(errorExecutor{err}, 1234, "/tmp", time.Second)
		if e != nil {
			t.Fatal(e)
		}
		_, e = s.Execute(context.Background(), "podman", nil, nil, true)
		var re *Error
		if !errors.As(e, &re) || strings.Contains(e.Error(), "private-secret") {
			t.Fatalf("unsafe error: %v", e)
		}
	}
}

func TestExecuteIgnoresAmbientRuntimeOverrides(t *testing.T) {
	for _, key := range []string{"CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINERS_CONF", "CONTAINERS_STORAGE_CONF", "DOCKER_HOST", "LD_PRELOAD", "LD_LIBRARY_PATH", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "HOME", "USER", "LOGNAME"} {
		t.Setenv(key, "/ambient-poison")
	}
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "env"), []byte("#!/bin/sh\necho AMBIENT_EXECUTABLE\n"), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", dir)
	r, e := (ExecRunner{}).Execute(context.Background(), Command{Path: "env", Timeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(r.Stdout, "ambient-poison") || strings.Contains(r.Stdout, "AMBIENT_EXECUTABLE") {
		t.Fatal("ambient environment or executable reached child")
	}
	for _, key := range []string{"CONTAINER_HOST=", "CONTAINER_CONNECTION=", "CONTAINERS_CONF=", "CONTAINERS_STORAGE_CONF=", "DOCKER_HOST=", "LD_PRELOAD=", "LD_LIBRARY_PATH="} {
		if strings.Contains(r.Stdout, key) {
			t.Fatalf("inherited %s", key)
		}
	}
	if !strings.Contains(r.Stdout, "PATH=/usr/bin:/bin\n") {
		t.Fatal("uncontrolled child search path")
	}
}

type helperExecutor struct{}

func (helperExecutor) Execute(ctx context.Context, c Command) (Result, error) {
	if len(c.Args) > 0 {
		c.Args = append(c.Args[:len(c.Args)-1], "BRINE_COMMAND_HELPER=1", c.Args[len(c.Args)-1])
	}
	return (ExecRunner{}).Execute(ctx, c)
}

func TestExecuteRejectsExplicitRuntimeRedirects(t *testing.T) {
	for _, key := range []string{"CONTAINER_HOST", "CONTAINERS_CONF", "LD_PRELOAD", "PATH", "HOME", "XDG_CONFIG_HOME"} {
		_, e := (ExecRunner{}).Execute(context.Background(), Command{Path: "env", Env: []string{key + "=/untrusted"}, Timeout: time.Second})
		var re *Error
		if !errors.As(e, &re) || re.Kind != Invalid {
			t.Fatalf("explicit override accepted: %s", key)
		}
	}
}
