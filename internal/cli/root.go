package cli

import (
	"context"
	"io"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
	"github.com/spf13/cobra"
)

const Version = "0.1.1-dev"

// Dependencies supplies the process resources and services used by commands.
// Callers provide every field. Execute owns presentation; callers own exit status.
type Dependencies struct {
	Context                context.Context
	Stdin                  io.Reader // Borrowed exclusively during secret reads; see readSecretInput.
	Stdout                 io.Writer
	Stderr                 io.Writer
	Version                string
	LookPath               func(string) (string, error)
	RunTUI                 func(context.Context, io.Reader, io.Writer) error
	DoctorRunner           localexec.Runner // Optional; nil uses bounded local execution.
	HostServerFactory      dispatch.Factory
	HostPlanner            dispatch.Planner
	HostInventory          dispatch.Inventory
	HostUID                func() int // Optional; nil reads the effective process UID.
	OriginalCommandLength  int
	HostJobs               dispatch.JobOperations
	HostAuthorization      dispatch.Authorization
	HostReconciler         dispatch.ReconcileOperations
	HostOperationRunner    OperationRunner
	OperationClient        OperationClient
	LoadOperationTarget    func(string, string) (transport.Target, error)
	HostLogs               dispatch.LogReader
	LogsClient             LogsClient
	HostApps               dispatch.AppOperations
	HostDiagnose           dispatch.DiagnosticReader
	HostConfig             dispatch.ConfigurationOperations
	HostSecrets            dispatch.SecretOperations
	HostWriterAttempt      func(context.Context, string) error
	HostBackupCredentials  dispatch.BackupCredentialOperations
	HostDataInitialization dispatch.DataInitializationOperations
	HostPermits            replication.LaunchReader
}

// NewRootCommand builds an independent command tree without executing it.
// Use Execute for the complete response contract, including parser failures.
func NewRootCommand(deps Dependencies) *cobra.Command {
	var jsonOutput bool
	var modes machineModes
	var noInput bool

	root := &cobra.Command{
		Use:           "brine",
		Short:         "Agent-first self-hosted deployments",
		Long:          "Deploy stateless apps to enrolled Linux servers through Podman, Quadlet/systemd and Caddy. Plan before apply and save the idempotency key. Acceptance is not completion; poll the operation ID. Diagnose and reconcile uncertain outcomes before retrying. Persistent app data and Litestream/R2 restores remain planned. The Charm TUI currently provides a welcome screen.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if modes.json && modes.jsonl {
				return result.New(result.InvalidUsage, nil)
			}
			jsonOutput = modes.enabled()
			return cmd.Context().Err()
		},
	}
	root.SetFlagErrorFunc(usageError)
	root.SetContext(deps.Context)
	root.SetIn(deps.Stdin)
	root.SetOut(humanOutput(deps.Stdout))
	root.SetErr(deps.Stderr)
	root.PersistentFlags().BoolVar(&modes.json, "json", false, "Machine-readable JSON output")
	root.PersistentFlags().BoolVar(&modes.jsonl, "jsonl", false, "Machine-readable JSON event stream")
	root.PersistentFlags().BoolVar(&noInput, "no-input", false, "Never request interactive input")
	root.AddCommand(newHostCmd(deps), newBackupCmd(deps, &modes), newDataCmd(deps, &modes))
	root.AddCommand(newConfigCmd(deps, &modes), newSecretCmd(deps, &modes), newLifecycleCmd(deps, &modes, "restart", plan.RestartApp), newLifecycleCmd(deps, &modes, "stop", plan.StopApp), newLifecycleCmd(deps, &modes, "start", plan.StartApp), newLifecycleCmd(deps, &modes, "remove", plan.RemoveApp))
	root.AddCommand(newReconcileCmd(&jsonOutput, deps))
	root.AddCommand(newResolveCmd(&jsonOutput, deps))
	root.AddCommand(newLogsCmd(deps, &modes))
	root.AddCommand(newDiagnoseCmd(deps, &modes))
	root.AddCommand(newEnrollCmd(deps, &noInput, &modes))
	root.AddCommand(newDoctorCmd(&jsonOutput, deps.LookPath, deps.DoctorRunner))
	root.AddCommand(newVersionCmd(&jsonOutput, deps.Version))
	root.AddCommand(newApplyCmd(&jsonOutput, deps), newOperationStatusCmd(&jsonOutput, &modes, deps), newRollbackCmd(deps, &modes))
	root.AddCommand(newValidateCmd(&jsonOutput))
	root.AddCommand(newPlanCmd(&jsonOutput, deps.Version, deps))
	root.AddCommand(newTUICmd(&jsonOutput, &noInput, deps))
	for _, cmd := range root.Commands() {
		if cmd.Args == nil {
			cmd.Args = cobra.NoArgs
		}
	}
	root.SetHelpCommand(&cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, remaining, err := cmd.Root().Find(args)
			if err != nil || len(remaining) != 0 {
				return result.New(result.InvalidUsage, err)
			}
			target.InitDefaultHelpFlag()
			return target.Help()
		},
	})
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	wrapArgumentValidators(root)
	return root
}

func wrapArgumentValidators(cmd *cobra.Command) {
	if validate := cmd.Args; validate != nil {
		cmd.Args = func(cmd *cobra.Command, args []string) error {
			if err := validate(cmd, args); err != nil {
				return result.New(result.InvalidUsage, err)
			}
			return nil
		}
	}
	for _, child := range cmd.Commands() {
		wrapArgumentValidators(child)
	}
}
