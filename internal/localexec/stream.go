package localexec

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RunStreams never returns child output. The caller owns bounded input/output
// streams; a stream or diagnostic quota error cancels the whole process group.
func (ExecRunner) RunStreams(ctx context.Context, command Command, input io.Reader, output io.Writer) error {
	if _, ok := ctx.Deadline(); !ok || command.OutputLimit <= 0 || output == nil {
		return &Error{Kind: Invalid}
	}
	path, err := LookPath(command.Path)
	if err != nil {
		return &Error{Kind: Invalid}
	}
	if !filepath.IsAbs(command.Dir) || !validStreamEnvironment(command.Env) {
		return &Error{Kind: Invalid}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if ctx.Err() != nil {
		return &Error{Kind: Timeout}
	}
	// #nosec G204 -- Separate typed argv, never a shell. Adapters own executable admission.
	cmd := exec.CommandContext(ctx, path, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env, cmd.Stdin = command.Env, input
	stdout := &cancelWriter{writer: output, cancel: cancel}
	diagnostics := &discardLimit{remaining: command.OutputLimit}
	stderr := &cancelWriter{writer: diagnostics, cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	configureProcessGroup(cmd)
	cmd.WaitDelay = 100 * time.Millisecond
	if err := cmd.Start(); err != nil {
		return &Error{Kind: Failed}
	}
	defer cleanupProcessGroup(cmd)
	err = cmd.Wait()
	if stdout.err != nil || stderr.err != nil {
		return &Error{Kind: streamFailureKind(command.Mutation), cause: ErrOutputLimit}
	}
	if ctx.Err() != nil {
		return &Error{Kind: streamFailureKind(command.Mutation)}
	}
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if command.Mutation && (!errors.As(err, &exit) || cmd.ProcessState.ExitCode() < 0) {
		return &Error{Kind: UnknownOutcome}
	}
	return &Error{Kind: Failed, ExitCode: cmd.ProcessState.ExitCode()}
}

func validStreamEnvironment(environment []string) bool {
	if len(environment) == 0 {
		return false
	}
	seen := make(map[string]bool, len(environment))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || seen[key] || strings.ContainsRune(entry, 0) {
			return false
		}
		seen[key] = true
	}
	return true
}

func streamFailureKind(mutation bool) ErrorKind {
	if mutation {
		return UnknownOutcome
	}
	return Timeout
}

type cancelWriter struct {
	writer io.Writer
	cancel context.CancelFunc
	err    error
}

func (w *cancelWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if err != nil {
		w.err = err
		w.cancel()
	}
	return n, err
}

type discardLimit struct{ remaining int }

func (w *discardLimit) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, ErrOutputLimit
	}
	w.remaining -= len(p)
	return len(p), nil
}
