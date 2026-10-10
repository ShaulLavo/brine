package main

import (
	"context"
	"fmt"
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
		HostWriterAttempt:     host.WriterAttempt,
		LookPath:              exec.LookPath,
		RunTUI: func(ctx context.Context, _ io.Reader, stdout io.Writer) error {
			return cli.RunTUI(ctx, nil, stdout)
		},
	}
	var closeRuntime func() error
	if cli.HostServeRequested(os.Args[1:]) {
		factory := host.NewServerFactory(deps.Version, authenticated)
		deps.HostServerFactory = factory.Build
		closeRuntime = factory.Close
	} else if cli.HostRuntimeRequested(os.Args[1:]) {
		open := host.Open
		if cli.HostReconcilePreviewRequested(os.Args[1:]) {
			open = host.OpenPreview
		}
		runtime, err := open(ctx, authenticated)
		if err == nil {
			closeRuntime = runtime.Close
			deps.HostOperationRunner = runtime.Runner
			deps.HostReconciler = runtime.Reconciler
		} else {
			fmt.Fprintln(os.Stderr, "Host runtime initialization failed:", result.Classify(err).Code())
		}
	}
	code := run(deps, os.Args[1:])
	if closeRuntime != nil {
		if err := closeRuntime(); err != nil && code == 0 {
			code = result.ExitCode(result.New(result.InternalError, err))
		}
	}
	stop()
	os.Exit(code)
}

func run(deps cli.Dependencies, args []string) int {
	return result.ExitCode(cli.Execute(deps, args))
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
