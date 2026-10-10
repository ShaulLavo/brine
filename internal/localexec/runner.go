// Package localexec runs typed local processes with deadlines and bounded output.
package localexec

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

const OutputLimit = 4096

// Runner accepts an executable path and separate arguments, never a shell string.
type Runner interface {
	Run(context.Context, string, ...string) (string, error)
}

// StdoutRunner keeps diagnostics out of machine-readable probe output.
type StdoutRunner interface {
	RunStdout(context.Context, string, ...string) (string, error)
}

// Capture retains stdout and reports overflow independently of its retained length.
type Capture struct {
	Stdout   string
	Overflow bool
}

type CaptureRunner interface {
	CaptureStdout(context.Context, int, string, ...string) (Capture, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, path string, args ...string) (string, error) {
	out, err := run(ctx, false, OutputLimit, path, args...)
	return out.Stdout, err
}
func (ExecRunner) RunStdout(ctx context.Context, path string, args ...string) (string, error) {
	out, err := run(ctx, true, OutputLimit, path, args...)
	return out.Stdout, err
}
func (ExecRunner) CaptureStdout(ctx context.Context, limit int, path string, args ...string) (Capture, error) {
	if limit <= 0 {
		return Capture{}, ErrOutputLimit
	}
	return run(ctx, true, limit, path, args...)
}

func run(ctx context.Context, stdoutOnly bool, limit int, path string, args ...string) (Capture, error) {
	path, err := LookPath(path)
	if err != nil {
		return Capture{}, err
	}
	env, home, err := commandEnvironment(nil, false)
	if err != nil {
		return Capture{}, err
	}
	output := &boundedOutput{limit: limit}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.Dir = home
	configureProcessGroup(cmd)
	cmd.Stdout = output
	cmd.Stderr = output
	if stdoutOnly {
		cmd.Stderr = &boundedOutput{limit: OutputLimit}
	}
	// Bound pipe draining if a descendant keeps the output descriptors open.
	cmd.WaitDelay = 100 * time.Millisecond
	err = cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return output.snapshot(), err
}

type boundedOutput struct {
	mu       sync.Mutex
	data     []byte
	limit    int
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := b.limit - len(b.data)
	if len(p) > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (b *boundedOutput) snapshot() Capture {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Capture{Stdout: string(b.data), Overflow: b.overflow}
}
