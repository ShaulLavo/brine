//go:build !unix

package main

import "os/exec"

func configureCommandTestBuild(command *exec.Cmd) {}

func stopCommandTestBuild(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}
