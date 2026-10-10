//go:build linux

package cli

import "golang.org/x/sys/unix"

func replaceReplicaProcess(file string, args, env []string) error { return unix.Exec(file, args, env) }
