package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newSQLiteRetireCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	root := &cobra.Command{Use: "sqlite", Short: "Inspect and resolve prepared SQLite removal without deleting data"}
	root.AddCommand(newSQLiteRetireInspectCmd(deps, modes), newSQLiteRetireResolveCmd(deps, modes))
	return root
}
func newSQLiteRetireInspectCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	cmd := &cobra.Command{Use: "inspect ARCHIVE_ID --target NAME", Short: "Inspect preparation and disposition progress, including before a manifest exists", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "sqlite_retire_inspect", dispatch.SQLiteRetireInspectArgs{ArchiveID: data.ArchiveID(args[0])})
		if err != nil {
			return err
		}
		i, ok := response.Data.(archive.Inspection)
		if !response.OK || !ok || string(i.Disposition.ArchiveID) != args[0] {
			return result.New(result.TransportInvalidResponse, nil)
		}
		return printSQLiteRemovalInspection(cmd, i, *modes)
	}}
	flags.register(cmd)
	return cmd
}
func printSQLiteRemovalInspection(cmd *cobra.Command, i archive.Inspection, modes machineModes) error {
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), i))
	}
	if i.Preparation != nil {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Archive %s. Preparation %s at sequence %d. Manifest=%t, abandon=%t, continue=%t. %s\n", i.Disposition.ArchiveID, i.Preparation.Stage, i.Preparation.Sequence, i.Preparation.Manifest, i.Preparation.CanAbandon, i.CanReconcile, i.Preparation.ClosedReason)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Archive %s. Disposition %s.\n", i.Disposition.ArchiveID, i.Disposition.Stage)
	return err
}
func newSQLiteRetireResolveCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags nativeTaskFlags
	var forward, abandon bool
	cmd := &cobra.Command{Use: "resolve OPERATION_ID --continue|--abandon --target NAME", Short: "Continue retirement or safely abandon the original removal attempt", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !jobs.ValidID(args[0]) || forward == abandon {
			return result.New(result.InvalidUsage, nil)
		}
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		disposition := "continue"
		if abandon {
			disposition = "abandon"
		}
		return resolveSQLiteRetireCommand(cmd, deps, *modes, flags, args[0], disposition)
	}}
	flags.register(cmd)
	cmd.Flags().BoolVar(&forward, "continue", false, "Continue with fresh original-source verification")
	cmd.Flags().BoolVar(&abandon, "abandon", false, "Release the original fences only if runtime and data are unchanged")
	return cmd
}
func resolveSQLiteRetireCommand(cmd *cobra.Command, deps Dependencies, modes machineModes, flags nativeTaskFlags, original, disposition string) error {
	response, err := flags.operation.call(cmd.Context(), deps, "sqlite_retire_resolve", dispatch.SQLiteRetireResolveArgs{OperationID: original, Disposition: disposition, IdempotencyKey: flags.key})
	if err != nil {
		return err
	}
	accepted, ok := response.Data.(jobs.Accepted)
	if !response.OK || !ok {
		return result.New(result.TransportInvalidResponse, nil)
	}
	value, err := flags.operation.waitTask(cmd.Context(), deps, accepted, ops.SQLiteRetireResolve, flags.noWait)
	if err != nil {
		return err
	}
	if flags.noWait {
		return printSQLiteRetireAccepted(cmd, modes, accepted)
	}
	receipt, ok := value.(archive.RemovalReceipt)
	if !ok || receipt.OperationID != accepted.OperationID || receipt.AttemptOperationID != original || disposition == "abandon" && receipt.Outcome != "abandoned" || disposition == "continue" && receipt.Outcome != "retired" {
		return result.New(result.TransportInvalidResponse, nil)
	}
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), receipt))
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Archive %s %s. Original operation %s remains recovery-required. No service was started and no data was deleted.\n", receipt.ArchiveID, receipt.Outcome, original)
	return err
}
func printSQLiteRetireAccepted(cmd *cobra.Command, modes machineModes, accepted jobs.Accepted) error {
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), accepted))
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Accepted SQLite archive resolution %s.\n", accepted.OperationID)
	return err
}
