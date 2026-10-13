//go:build linux

package localexec

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type ForegroundCommand struct {
	Path   string
	Args   []string
	Env    []string
	Dir    string
	Stderr io.Writer
}

// ForegroundProcess keeps wait and signal authority at the subprocess boundary.
// Its caller owns the context and must independently prove container absence.
type ForegroundProcess struct {
	command *exec.Cmd
	done    chan error
}

func StartForeground(ctx context.Context, request ForegroundCommand) (*ForegroundProcess, error) {
	if !filepath.IsAbs(request.Path) || !filepath.IsAbs(request.Dir) || len(request.Args) > 128 || len(request.Env) == 0 || len(request.Env) > 32 || ctx.Err() != nil {
		return nil, &Error{Kind: Invalid}
	}
	for _, value := range append(append([]string{request.Path, request.Dir}, request.Args...), request.Env...) {
		if len(value) > 8192 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, &Error{Kind: Invalid}
		}
	}
	command := exec.CommandContext(ctx, request.Path, request.Args...) //nolint:gosec // Typed bounded argv at the subprocess boundary; no shell expansion.
	command.Env = append([]string(nil), request.Env...)
	command.Dir = request.Dir
	command.Stderr = request.Stderr
	configureProcessGroup(command)
	command.WaitDelay = time.Second
	if err := command.Start(); err != nil {
		return nil, &Error{Kind: Failed, cause: err}
	}
	process := &ForegroundProcess{command: command, done: make(chan error, 1)}
	go func() { process.done <- command.Wait() }()
	return process, nil
}

func (p *ForegroundProcess) Done() <-chan error { return p.done }

func (p *ForegroundProcess) PID() int { return p.command.Process.Pid }

func (p *ForegroundProcess) Interrupt() error {
	if p == nil || p.command.Process == nil {
		return &Error{Kind: Invalid}
	}
	return p.command.Process.Signal(syscall.SIGINT)
}

func (p *ForegroundProcess) KillGroup() error {
	if p == nil || p.command.Process == nil {
		return &Error{Kind: Invalid}
	}
	if err := p.command.Process.Signal(syscall.Signal(0)); err != nil {
		return err
	}
	return p.command.Cancel()
}
