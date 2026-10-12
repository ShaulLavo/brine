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

func newNativeRetireCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	root := &cobra.Command{Use: "native", Short: "Archive an exactly prepared SpacetimeDB app and preserve its recovery history"}
	root.AddCommand(newNativeRetirePlanCmd(deps, modes), newNativeRetireApplyCmd(deps, modes), newNativeRetireInspectCmd(deps, modes), newNativeRetireResolveCmd(deps, modes))
	return root
}

func newNativeRetireResolveCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags nativeTaskFlags
	cmd := &cobra.Command{Use: "resolve OPERATION_ID --target NAME", Short: "Reconcile retained archive effects after fresh remote verification", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !jobs.ValidID(args[0]) {
			return result.New(result.InvalidUsage, nil)
		}
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		response, err := flags.operation.call(cmd.Context(), deps, "native_retire_resolve", dispatch.NativeRetireResolveArgs{OperationID: args[0], IdempotencyKey: flags.key})
		if err != nil {
			return err
		}
		accepted, ok := response.Data.(jobs.Accepted)
		if !ok || !response.OK || accepted.Status != "accepted" || !jobs.ValidID(accepted.OperationID) {
			return result.New(result.TransportInvalidResponse, nil)
		}
		value, err := flags.operation.waitTask(cmd.Context(), deps, accepted, ops.NativeRetireResolve, flags.noWait)
		if err != nil {
			return err
		}
		if flags.noWait {
			if modes.enabled() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Accepted archive resolution %s.\n", accepted.OperationID)
			return err
		}
		receipt, ok := value.(archive.TaskReceipt)
		if !ok || receipt.OperationID != accepted.OperationID || receipt.OriginalOperationID != args[0] || receipt.Outcome != "retired" {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), receipt))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Resolved archive %s. Original operation %s remains recovery-required.\n", receipt.ArchiveID, receipt.OriginalOperationID)
		return err
	}}
	flags.register(cmd)
	return cmd
}
func newNativeRetirePlanCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	cmd := &cobra.Command{Use: "plan APP --target NAME", Short: "Review the current exact point, all epochs and terminal recovery trees", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dispatch.ValidApp(args[0]) || !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "native_retire_plan", dispatch.NativeRetirePlanArgs{App: args[0]})
		if err != nil {
			return err
		}
		p, ok := response.Data.(archive.Plan)
		if !ok || !response.OK || !p.Valid() || p.Candidate.App != args[0] {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), p))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Archive plan %s. Archive %s preserves current data, prior recovery trees and all remote epochs.\n", p.ID, p.ArchiveID)
		return err
	}}
	flags.register(cmd)
	return cmd
}
func newNativeRetireApplyCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags nativeTaskFlags
	var plan string
	cmd := &cobra.Command{Use: "apply APP --plan ID --target NAME", Short: "Retire owned runtime and retain the full data archive for thirty days", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dispatch.ValidApp(args[0]) || !jobs.ValidPlanID(plan) {
			return result.New(result.InvalidUsage, nil)
		}
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		response, err := flags.operation.call(cmd.Context(), deps, "native_retire", dispatch.NativeRetireApplyArgs{App: args[0], PlanID: plan, IdempotencyKey: flags.key})
		if err != nil {
			return err
		}
		accepted, ok := response.Data.(jobs.Accepted)
		if !ok || !response.OK || accepted.Status != "accepted" || !jobs.ValidID(accepted.OperationID) {
			return result.New(result.TransportInvalidResponse, nil)
		}
		value, err := flags.operation.waitTask(cmd.Context(), deps, accepted, ops.NativeRetire, flags.noWait)
		if err != nil {
			return err
		}
		if flags.noWait {
			if modes.enabled() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Accepted archive operation %s.\n", accepted.OperationID)
			return err
		}
		receipt, ok := value.(archive.TaskReceipt)
		if !ok || receipt.PlanID != plan || receipt.OperationID != accepted.OperationID || receipt.Outcome != "retired" {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), receipt))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Retired app into archive %s. All remote epochs and recovery keys remain retained.\n", receipt.ArchiveID)
		return err
	}}
	flags.register(cmd)
	cmd.Flags().StringVar(&plan, "plan", "", "Reviewed immutable archive plan")
	return cmd
}
func newNativeRetireInspectCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	cmd := &cobra.Command{Use: "inspect ARCHIVE_ID --target NAME", Short: "Inspect retained archive effects before recovery", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) || !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "native_retire_inspect", dispatch.NativeRetireInspectArgs{ArchiveID: data.ArchiveID(args[0])})
		if err != nil {
			return err
		}
		inspection, ok := response.Data.(archive.Inspection)
		if !ok || !response.OK || inspection.Disposition.ArchiveID != data.ArchiveID(args[0]) {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), inspection))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Archive %s: %s.\n", inspection.Disposition.ArchiveID, inspection.Disposition.Stage)
		return err
	}}
	flags.register(cmd)
	return cmd
}
