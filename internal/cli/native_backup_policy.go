package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newNativeBackupPolicyCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	group := &cobra.Command{Use: "policy", Short: "Review and activate native backup cadence and recovery material"}
	var planFlags operationFlags
	var deploymentPlan string
	plan := &cobra.Command{Use: "plan --deployment-plan ID --target NAME", Short: "Save a native backup revision from a reviewed deployment plan", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !jobs.ValidPlanID(deploymentPlan) || !planFlags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := planFlags.call(cmd.Context(), deps, "native_backup_policy_plan", dispatch.NativeBackupPolicyPlanArgs{DeploymentPlanID: deploymentPlan})
		if err != nil {
			return err
		}
		view, ok := response.Data.(dispatch.NativeBackupPolicyPlanView)
		if !response.OK || !ok || !view.Valid() {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), view))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Native backup plan %s captures every %d seconds. Activate with %s.\n", view.PlanID, view.CaptureIntervalSeconds, planFlags.command("backup", "policy", "activate", view.PlanID))
		return err
	}}
	planFlags.register(plan)
	plan.Flags().StringVar(&deploymentPlan, "deployment-plan", "", "Saved managed deployment plan selecting backup policy and destination")
	var flags nativeTaskFlags
	activate := &cobra.Command{Use: "activate PLAN_ID --target NAME", Short: "Authenticate the owned repository and install fixed backup units", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) {
			return result.New(result.InvalidUsage, nil)
		}
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		response, err := flags.operation.call(cmd.Context(), deps, string(ops.NativeBackupActivate), dispatch.NativeBackupPolicyActivateArgs{PlanID: args[0], IdempotencyKey: flags.key})
		if err != nil {
			return err
		}
		accepted, ok := response.Data.(jobs.Accepted)
		if !response.OK || !ok || accepted.Status != "accepted" || !jobs.ValidID(accepted.OperationID) {
			return result.New(result.TransportInvalidResponse, nil)
		}
		value, err := flags.operation.waitTask(cmd.Context(), deps, accepted, ops.NativeBackupActivate, flags.noWait)
		if err != nil {
			return err
		}
		return writeNativeBackupActivationResult(cmd, modes, value, accepted, args[0], flags)
	}}
	flags.register(activate)
	group.AddCommand(plan, activate)
	return group
}

func writeNativeBackupActivationResult(cmd *cobra.Command, modes *machineModes, value any, accepted jobs.Accepted, planID string, flags nativeTaskFlags) error {
	if flags.noWait {
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Accepted operation %s. Check with %s.\n", accepted.OperationID, flags.operation.command("status", "--operation", accepted.OperationID))
		return err
	}
	receipt, ok := value.(nativecapture.BackupPolicyReceipt)
	if !ok || receipt.OperationID != accepted.OperationID || receipt.PlanID != planID {
		return result.New(result.TransportInvalidResponse, nil)
	}
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), receipt))
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Native backup revision %d is active for binding %s.\n", receipt.Revision, receipt.BindingID)
	return err
}
