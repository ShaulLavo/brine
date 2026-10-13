//go:build linux

package localexec

import (
	"errors"
	"os/exec"
	"syscall"
)

type ExitReason uint8

const (
	ExitUnknown ExitReason = iota
	ExitNonzero
	ExitInterrupt
	ExitKilled
	ExitSignal
)

// InspectExitReason reports only a closed reason, without exposing native output.
func InspectExitReason(err error) ExitReason {
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ProcessState == nil {
		return ExitUnknown
	}
	status, ok := exited.Sys().(syscall.WaitStatus)
	if !ok {
		return ExitUnknown
	}
	if !status.Signaled() {
		return ExitNonzero
	}
	switch status.Signal() {
	case syscall.SIGINT:
		return ExitInterrupt
	case syscall.SIGKILL:
		return ExitKilled
	default:
		return ExitSignal
	}
}
