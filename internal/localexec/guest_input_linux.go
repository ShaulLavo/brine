//go:build linux

package localexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// GuestRunner uses the private container's fixed environment without host
// account lookup. The enclosing purpose proves mounts and pinned executable.
type GuestRunner struct{}

func (GuestRunner) RunInput(ctx context.Context, command Command) (Output, error) {
	if os.Getpid() != 1 || os.Geteuid() != 1000 || os.Getegid() != 1000 || !filepath.IsAbs(command.Path) || command.Dir != "/tmp" || len(command.Env) != 0 || command.OutputLimit <= 0 || command.OutputLimit > 1<<20 || len(command.Args) > 128 {
		return Output{}, &Error{Kind: Invalid}
	}
	for _, argument := range command.Args {
		if len(argument) > 8192 || strings.ContainsAny(argument, "\x00\r\n") {
			return Output{}, &Error{Kind: Invalid}
		}
	}
	return runInputCommand(ctx, command.Path, command, guestInputEnvironment())
}

func guestInputEnvironment() []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", "TOKIO_WORKER_THREADS=2"}
}
