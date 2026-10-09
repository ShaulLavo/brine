package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/ShaulLavo/brine/internal/cli"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/host"
	"github.com/ShaulLavo/brine/internal/result"
)

func main() {
	authenticated := ""
	originalCommandLength := 0
	var stdin io.Reader = os.Stdin
	if cli.HostServeRequested(os.Args[1:]) {
		authenticated = os.Getenv("BRINE_AUTHENTICATED")
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
	var closeRuntime func() error
	if cli.HostServeRequested(os.Args[1:]) {
		deps.HostServerFactory = func(ctx context.Context, op string) (*dispatch.Server, error) {
			if op == "ping" {
				return nil, nil
			}
			if authenticated != "deploy" {
				return nil, result.New(result.DispatchOperationRefused, nil)
			}
			if op == "inventory" {
				collector, err := host.NewInventory(ctx)
				if err != nil {
					return nil, err
				}
				return dispatch.NewServer(deps.Version, collector), nil
			}
			runtime, err := host.Open(ctx, authenticated)
			if err != nil {
				return nil, err
			}
			closeRuntime = runtime.Close
			server := dispatch.NewServer(deps.Version, runtime.Inventory).WithJobs(runtime.Jobs, runtime.Authorize)
			server.Planner = runtime.Planner
			server.Apps = runtime.Apps
			server.Logs = runtime.Logs
			server.Diagnose = runtime.Diagnose
			return server, nil
		}
	} else if cli.HostRuntimeRequested(os.Args[1:]) {
		runtime, err := host.Open(ctx, authenticated)
		if err == nil {
			closeRuntime = runtime.Close
			deps.HostOperationRunner = runtime.Runner
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
