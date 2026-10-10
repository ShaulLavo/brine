package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/ShaulLavo/brine/internal/cli"
	"github.com/ShaulLavo/brine/internal/host"
	"github.com/ShaulLavo/brine/internal/result"
)

func main() {
	authenticated, originalCommandLength := captureHostEnvironment(os.Args[1:], os.Getenv, os.Clearenv)
	var stdin io.Reader = os.Stdin
	if cli.HostServeRequested(os.Args[1:]) {
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
	code := runWithRuntime(deps, os.Args[1:], authenticated)

	stop()
	os.Exit(code)
}

func run(deps cli.Dependencies, args []string) int {
	return result.ExitCode(cli.Execute(deps, args))
}

func runWithRuntime(deps cli.Dependencies, args []string, authenticated string) int {
	lifecycle := cli.RuntimeLifecycle{}
	if cli.HostServeRequested(args) {
		factory := host.NewServerFactory(deps.Version, authenticated)
		deps.HostServerFactory = factory.Build
		lifecycle.Close = factory.Close
	} else {
		lifecycle.Open = func(ctx context.Context, preview bool) (cli.RuntimeServices, error) {
			open := host.Open
			if preview {
				open = host.OpenPreview
			}
			runtime, err := open(ctx, authenticated)
			if err != nil {
				return cli.RuntimeServices{}, err
			}
			return cli.RuntimeServices{Runner: runtime.Runner, Reconciler: runtime.Reconciler, Close: runtime.Close}, nil
		}
	}
	return result.ExitCode(cli.ExecuteWithRuntime(deps, args, lifecycle))
}

type failedInput struct{ err error }

func (f failedInput) Read([]byte) (int, error) { return 0, f.err }
func (failedInput) Close() error               { return nil }

func captureHostEnvironment(args []string, getenv func(string) string, clearenv func()) (string, int) {
	if !cli.HostServeRequested(args) {
		return "", 0
	}
	marker := getenv("BRINE_AUTHENTICATED")
	length := len(getenv("SSH_ORIGINAL_COMMAND"))
	clearenv()
	return marker, length
}
