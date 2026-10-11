//go:build unix

package localexec

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

func cleanupProcessGroup(cmd *exec.Cmd) {
	// Streaming adapters own this group, including any descendants still alive
	// after the immediate child exits or its pipes exceed WaitDelay.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
