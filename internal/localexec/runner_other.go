//go:build !unix

package localexec

import "os/exec"

func configureProcessGroup(cmd *exec.Cmd) {
	// Non-Unix platforms retain CommandContext's immediate-process cancellation.
}
