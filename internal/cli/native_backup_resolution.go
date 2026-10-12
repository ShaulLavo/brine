package cli

import (
	"encoding/json"
	"fmt"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
	"regexp"
)

func addNativeBackupResolutionCommands(root *cobra.Command, deps Dependencies, modes *machineModes) {
	for _, mode := range []string{"inspect", "review"} {
		var flags operationFlags
		command := &cobra.Command{Use: mode + " OPERATION_ID --target NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			if !jobs.ValidID(args[0]) || !flags.validSelection() {
				return result.New(result.InvalidUsage, nil)
			}
			var input any = dispatch.NativeBackupInspectArgs{OperationID: args[0]}
			if mode == "review" {
				input = dispatch.NativeBackupReviewArgs{OperationID: args[0]}
			}
			response, err := flags.call(cmd.Context(), deps, "native_backup_"+mode, input)
			if err != nil {
				return err
			}
			if !response.OK {
				return result.New(result.TransportInvalidResponse, nil)
			}
			if modes.enabled() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), response.Data))
			}
			if review, ok := response.Data.(nativecapture.BackupResolutionReview); ok {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Review %s expires %s. Original operation remains recovery_required.\n", review.ID, review.ExpiresAt.Format("2006-01-02T15:04:05Z"))
				return err
			}
			view, ok := response.Data.(dispatch.NativeBackupInspectionView)
			if !ok {
				return result.New(result.TransportInvalidResponse, nil)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Original operation %s: %s. No write was repeated.\n", view.OperationID, view.State)
			return err
		}}
		flags.register(command)
		root.AddCommand(command)
	}
	var flags nativeTaskFlags
	resolve := &cobra.Command{Use: "resolve REVIEW_ID --target NAME", Short: "Adopt the inspected remote effect into a new journaled operation", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(args[0]) {
			return result.New(result.InvalidUsage, nil)
		}
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		response, err := flags.operation.call(cmd.Context(), deps, string(ops.NativeBackupResolve), dispatch.NativeBackupResolveArgs{ReviewID: args[0], IdempotencyKey: flags.key})
		if err != nil {
			return err
		}
		accepted, ok := response.Data.(jobs.Accepted)
		if !response.OK || !ok {
			return result.New(result.TransportInvalidResponse, nil)
		}
		value, err := flags.operation.waitTask(cmd.Context(), deps, accepted, ops.NativeBackupResolve, flags.noWait)
		if err != nil {
			return err
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
		}
		if flags.noWait {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Accepted operation %s.\n", accepted.OperationID)
			return err
		}
		receipt, ok := value.(nativecapture.BackupResolutionReceipt)
		if !ok {
			return result.New(result.TransportInvalidResponse, nil)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Adopted original operation %s through %s. Original outcome remains unchanged.\n", receipt.OriginalOperationID, receipt.OperationID)
		return err
	}}
	flags.register(resolve)
	root.AddCommand(resolve)
}
