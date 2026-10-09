// Package localexec runs typed local subprocesses with bounded output.
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

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, path string, args ...string) (string, error) {
	return run(ctx, false, path, args...)
}
func (ExecRunner) RunStdout(ctx context.Context, path string, args ...string) (string, error) {
	return run(ctx, true, path, args...)
}
func run(ctx context.Context, stdoutOnly bool, path string, args ...string) (string, error) {
	output := &boundedOutput{}
	cmd := exec.CommandContext(ctx, path, args...)
	configureProcessGroup(cmd)
	cmd.Stdout = output
	cmd.Stderr = output
	if stdoutOnly {
		cmd.Stderr = &boundedOutput{}
	}
	// Bound pipe draining if a descendant keeps the output descriptors open.
	cmd.WaitDelay = 100 * time.Millisecond
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return output.String(), err
}

type boundedOutput struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if remaining := OutputLimit - len(b.data); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}

func (b *boundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
