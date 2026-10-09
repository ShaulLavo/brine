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
	if cli.HostRuntimeRequested(os.Args[1:]) {
		runtime, err := host.Open(ctx, authenticated)
		if err == nil {
			closeRuntime = runtime.Close
			deps.HostInventory = runtime.Inventory
			deps.HostPlanner = runtime.Planner
			deps.HostJobs = runtime.Jobs
			deps.HostAuthorization = runtime.Authorize
			deps.HostOperationRunner = runtime.Runner
			deps.HostApps = runtime.Apps
			deps.HostLogs = runtime.Logs
			deps.HostDiagnose = runtime.Diagnose
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
