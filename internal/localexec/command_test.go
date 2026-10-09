package localexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCommandHelper(t *testing.T) {
	if os.Getenv("BRINE_COMMAND_HELPER") != "1" {
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
	result, err := (ExecRunner{}).Execute(context.Background(), Command{Path: os.Args[0], Args: []string{"-test.run=^TestCommandHelper$", "--", "input"}, Stdin: []byte("secret"), Env: []string{"XDG_RUNTIME_DIR=/run/user/1234"}, Dir: dir, Timeout: 3 * time.Second})
	if err != nil || result.Stdout != "secret|/run/user/1234|"+dir || result.Stderr != "diagnostic" {
		t.Fatalf("unexpected execution result: %#v %v", result, err)
	}
}
func TestExecuteBoundsBothStreams(t *testing.T) {
	t.Setenv("BRINE_COMMAND_HELPER", "1")
	result, err := (ExecRunner{}).Execute(context.Background(), Command{Path: os.Args[0], Args: []string{"-test.run=^TestCommandHelper$", "--", "large"}, Timeout: 3 * time.Second})
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
		_, err := (ExecRunner{}).Execute(context.Background(), Command{Path: os.Args[0], Args: []string{"-test.run=^TestCommandHelper$", "--", tt.mode}, Timeout: timeout, Mutation: tt.mutation})
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
	s, e := NewSession(ExecRunner{}, 1234, t.TempDir(), 3*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	for _, mutation := range []bool{false, true} {
		_, e := s.Execute(context.Background(), os.Args[0], []string{"-test.run=^TestCommandHelper$", "--", "large"}, nil, mutation)
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
