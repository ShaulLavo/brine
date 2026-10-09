package localexec

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"
)

var ErrOutputLimit = errors.New("subprocess output exceeded limit")

type Command struct {
	Path        string
	Args        []string
	Stdin       []byte
	Env         []string
	OutputLimit int
}
type Output struct{ Stdout, Stderr []byte }
type InputRunner interface {
	RunInput(context.Context, Command) (Output, error)
}

// RunInput never inherits environment and keeps stdout separate from diagnostics.
func (ExecRunner) RunInput(ctx context.Context, command Command) (Output, error) {
	if command.OutputLimit <= 0 {
		return Output{}, ErrOutputLimit
	}
	stdout := &limitedCapture{limit: command.OutputLimit}
	stderr := &limitedCapture{limit: command.OutputLimit}
	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Env = append([]string{}, command.Env...)
	cmd.Stdin = bytes.NewReader(command.Stdin)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	configureProcessGroup(cmd)
	cmd.WaitDelay = 100 * time.Millisecond
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if stdout.overflow || stderr.overflow {
		err = ErrOutputLimit
	}
	return Output{stdout.data, stderr.data}, err
}

type limitedCapture struct {
	mu       sync.Mutex
	data     []byte
	limit    int
	overflow bool
}

func (b *limitedCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := b.limit - len(b.data)
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}
