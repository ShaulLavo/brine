package localexec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const CommandOutputLimit = 256 * 1024

type ErrorKind string

const (
	NotFound       ErrorKind = "not_found"
	Failed         ErrorKind = "failed"
	Timeout        ErrorKind = "timeout"
	UnknownOutcome ErrorKind = "unknown_outcome"
	Invalid        ErrorKind = "invalid_input"
)

// Error deliberately excludes subprocess output, arguments and underlying OS errors.
type Error struct {
	Kind     ErrorKind
	ExitCode int
	cause    error
}

func (e *Error) Error() string { return "runtime operation: " + string(e.Kind) }
func (e *Error) Unwrap() error { return e.cause }

type Command struct {
	Path     string
	Args     []string
	Stdin    []byte
	Env      []string
	Dir      string
	Timeout  time.Duration
	Mutation bool
}
type Result struct {
	Stdout, Stderr string
	ExitCode       int
	Truncated      bool
}
type Executor interface {
	Execute(context.Context, Command) (Result, error)
}

func (ExecRunner) Execute(ctx context.Context, c Command) (Result, error) {
	if c.Timeout <= 0 || c.Path == "" {
		return Result{}, &Error{Kind: Invalid}
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	if ctx.Err() != nil {
		return Result{}, &Error{Kind: Timeout, cause: ctx.Err()}
	}
	stdout, stderr := &commandOutput{}, &commandOutput{}
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	configureProcessGroup(cmd)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.Stdin = bytes.NewReader(c.Stdin)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 100 * time.Millisecond
	if err := cmd.Start(); err != nil {
		kind := Failed
		if ctx.Err() != nil {
			return Result{}, &Error{Kind: Timeout, cause: ctx.Err()}
		}
		return Result{}, &Error{Kind: kind, ExitCode: -1}
	}
	err := cmd.Wait()
	out, outTruncated := stdout.snapshot()
	errout, errTruncated := stderr.snapshot()
	result := Result{Stdout: out, Stderr: errout, ExitCode: cmd.ProcessState.ExitCode(), Truncated: outTruncated || errTruncated}
	if ctx.Err() != nil {
		kind := Timeout
		if c.Mutation {
			kind = UnknownOutcome
		}
		return result, &Error{Kind: kind, ExitCode: result.ExitCode, cause: ctx.Err()}
	}
	if err != nil {
		kind := Failed
		// A wait/drain failure after a successful start cannot prove a mutation failed.
		var exit *exec.ExitError
		if c.Mutation && (!errors.As(err, &exit) || result.ExitCode < 0) {
			kind = UnknownOutcome
		}
		return result, &Error{Kind: kind, ExitCode: result.ExitCode}
	}
	return result, nil
}

type commandOutput struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *commandOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := CommandOutputLimit - len(b.data)
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *commandOutput) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data), b.truncated
}

// Session supplies the non-login runner environment. It does not switch users.
// The dispatcher must already run under the enrolled runner account.
type Session struct {
	executor Executor
	dir      string
	env      []string
	timeout  time.Duration
}

func NewSession(executor Executor, uid uint32, dir string, timeout time.Duration) (Session, error) {
	if executor == nil || !filepath.IsAbs(dir) || timeout <= 0 {
		return Session{}, &Error{Kind: Invalid}
	}
	runtime := "/run/user/" + strconv.FormatUint(uint64(uid), 10)
	return Session{executor: executor, dir: dir, env: []string{"XDG_RUNTIME_DIR=" + runtime, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtime + "/bus", "LC_ALL=C"}, timeout: timeout}, nil
}
func (s Session) Execute(ctx context.Context, path string, args []string, stdin []byte, mutation bool) (Result, error) {
	if s.executor == nil {
		return Result{}, &Error{Kind: Invalid}
	}
	result, err := s.executor.Execute(ctx, Command{Path: path, Args: append([]string(nil), args...), Stdin: stdin, Env: append([]string(nil), s.env...), Dir: s.dir, Timeout: s.timeout, Mutation: mutation})
	if err != nil {
		var runtimeError *Error
		if errors.As(err, &runtimeError) {
			return result, runtimeError
		}
		kind := Failed
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			kind = Timeout
			if mutation {
				kind = UnknownOutcome
			}
		}
		return result, &Error{Kind: kind}
	}
	if result.Truncated {
		kind := Failed
		if mutation {
			kind = UnknownOutcome
		}
		return result, &Error{Kind: kind}
	}
	return result, nil
}
