package localexec

import (
	"errors"
	"os/exec"
)

type ExitStatus struct {
	Code     int
	Signaled bool
}

func InspectExit(err error) (ExitStatus, bool) {
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ProcessState == nil {
		return ExitStatus{}, false
	}
	return ExitStatus{Code: exited.ExitCode(), Signaled: !exited.ProcessState.Exited()}, true
}
