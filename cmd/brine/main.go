package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/ShaulLavo/brine/internal/cli"
	"github.com/ShaulLavo/brine/internal/result"
)

func main() {
	originalCommandLength := 0
	var stdin io.Reader = os.Stdin
	if len(os.Args) >= 3 && os.Args[1] == "host" && os.Args[2] == "serve" {
		originalCommandLength = len(os.Getenv("SSH_ORIGINAL_COMMAND"))
		os.Clearenv()
		input := hostInput()
		defer input.Close()
		stdin = input
	}
	// Let closed stdout pipes return EPIPE instead of terminating with SIGPIPE.
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	deps := cli.Dependencies{
		Context:               ctx,
		OriginalCommandLength: originalCommandLength,
		Stdin:                 stdin,
		Stdout:                os.Stdout,
		Stderr:                os.Stderr,
		Version:               cli.Version,
		LookPath:              exec.LookPath,
		RunTUI: func(ctx context.Context, _ io.Reader, stdout io.Writer) error {
			return cli.RunTUI(ctx, nil, stdout)
		},
	}
	code := run(deps, os.Args[1:])
	stop()
	os.Exit(code)
}

func run(deps cli.Dependencies, args []string) int {
	return result.ExitCode(cli.Execute(deps, args))
}

type failedInput struct{ err error }

func (f failedInput) Read([]byte) (int, error) { return 0, f.err }
func (failedInput) Close() error               { return nil }
