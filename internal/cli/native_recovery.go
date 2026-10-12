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
)

func newNativeRecoveryCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	group := &cobra.Command{Use: "recovery", Short: "Deliver independent recovery passwords for encrypted native backups"}
	var flags nativeTaskFlags
	var planID string
	set := &cobra.Command{Use: "set --deployment-plan ID --target NAME", Short: "Deliver a private password JSON packet on stdin", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !jobs.ValidPlanID(planID) || deps.Stdin == nil || isTerminal(deps.Stdin) {
			return result.New(result.InvalidUsage, nil)
		}
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		raw, err := readSecretInput(cmd.Context(), deps.Stdin)
		defer clear(raw)
		if err != nil || len(raw) > 16<<10 {
			return result.New(result.InvalidUsage, nil)
		}
		wire, err := dispatch.EncodeNativeRecoverySet(planID, flags.key, raw)
		defer clear(wire)
		if err != nil {
			return err
		}
		response, err := flags.operation.call(cmd.Context(), deps, string(ops.NativeRecoverySet), wire)
		if err != nil {
			return err
		}
		accepted, ok := response.Data.(jobs.Accepted)
		if !response.OK || !ok || accepted.Status != "accepted" || !jobs.ValidID(accepted.OperationID) {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if !flags.noWait {
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "operation_id=%s\n", accepted.OperationID); err != nil {
				return err
			}
		}
		value, err := flags.operation.waitTask(cmd.Context(), deps, accepted, ops.NativeRecoverySet, flags.noWait)
		if err != nil {
			return err
		}
		return writeNativeRecoveryResult(cmd, modes, value, accepted, flags)
	}}
	flags.register(set)
	set.Flags().StringVar(&planID, "deployment-plan", "", "Saved managed deployment plan selecting the recovery reference")
	group.AddCommand(set)
	return group
}

func writeNativeRecoveryResult(cmd *cobra.Command, modes *machineModes, value any, accepted jobs.Accepted, flags nativeTaskFlags) error {
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
	}
	if flags.noWait {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Accepted operation %s. Check with %s.\n", accepted.OperationID, flags.operation.command("status", "--operation", accepted.OperationID))
		return err
	}
	receipt, ok := value.(nativecapture.RecoveryReceipt)
	if !ok || receipt.OperationID != accepted.OperationID {
		return result.New(result.TransportInvalidResponse, nil)
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Stored recovery password %s version %d for native binding %s.\n", receipt.Reference, receipt.Version, receipt.BindingID)
	return err
}
