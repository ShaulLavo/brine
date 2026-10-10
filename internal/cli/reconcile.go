package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/spf13/cobra"
)

// HostReconcilePreviewRequested selects a non-writing initializer before Cobra
// runs. Invalid preview flags still cannot accidentally initialize the store.
func HostReconcilePreviewRequested(args []string) bool {
	if !HostRuntimeRequested(args) || !slices.Contains(args, "reconcile") {
		return false
	}
	for _, arg := range args {
		if arg == "--dry-run" || strings.HasPrefix(arg, "--dry-run=") && arg != "--dry-run=false" {
			return true
		}
	}
	return false
}

func newReconcileCmd(machine *bool, deps Dependencies) *cobra.Command {
	var flags operationFlags
	var dry bool
	cmd := &cobra.Command{Use: "reconcile --target NAME", Short: "Inspect interrupted operations and safely settle their outcomes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := flags.call(cmd.Context(), deps, "reconcile", dispatch.ReconcileArgs{DryRun: dry})
		if err != nil {
			return err
		}
		if !dry {
			accepted, ok := response.Data.(jobs.Accepted)
			if !response.OK || !ok || accepted.Status != "accepted" || !jobs.ValidID(accepted.OperationID) {
				return result.New(result.TransportInvalidResponse, nil)
			}
			if *machine {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), accepted))
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Recovery accepted: %s.\nPoll with: %s\n", accepted.OperationID, flags.command("status", "--operation", accepted.OperationID))
			return err
		}
		report, ok := response.Data.(reconcile.Report)
		if !response.OK || !ok || report.DryRun != dry {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if *machine {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), report))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Reconciliation inspected %d unfinished operations (dry-run: %t).\n", len(report.Outcomes), report.DryRun)
		if report.ControlState != "" && err == nil {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Control state: %s. No recovery assessment was made.\n", report.ControlState)
		}
		for _, outcome := range report.Outcomes {
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s -> %s (%s)\n", outcome.OperationID, outcome.Before, outcome.After, outcome.Action)
		}
		return err
	}}
	flags.register(cmd)
	cmd.Flags().BoolVar(&dry, "dry-run", false, "Preview recovery without writing events or applying effects")
	return cmd
}

// The boot unit runs as the enrolled local account, not through an SSH request.
// Runtime construction checks that account and the root-owned operator policy.
func newHostReconcileCmd(deps Dependencies) *cobra.Command {
	var dry bool
	cmd := &cobra.Command{Use: "reconcile", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		uid := deps.HostUID
		if uid == nil {
			uid = os.Geteuid
		}
		if uid() == 0 {
			return result.New(result.DispatchRootRefused, nil)
		}
		if deps.HostReconciler == nil {
			return result.New(result.DependencyMissing, nil)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), systemd.JobRuntimeLimit)
		defer cancel()
		var report reconcile.Report
		var err error
		if dry {
			report, err = deps.HostReconciler.DryRun(ctx)
		} else {
			report, err = deps.HostReconciler.Reconcile(ctx)
		}
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), report))
	}}
	cmd.Flags().BoolVar(&dry, "dry-run", false, "Preview local recovery without changes")
	return cmd
}
