package cli

import (
	"encoding/json"
	"fmt"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newNativeLiveRestoreCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	root := &cobra.Command{Use: "native-live", Short: "Review and restore an exact SpacetimeDB point into a new stopped epoch"}
	root.AddCommand(newNativeLiveRestorePlanCmd(deps, modes), newNativeLiveRestoreApplyCmd(deps, modes), newNativeLiveRestoreInspectCmd(deps, modes), newNativeRestoreResolutionReviewCmd(deps, modes), newNativeRestoreResolveCmd(deps, modes))
	return root
}

func newNativeLiveRestorePlanCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	var request liverestore.PlanRequest
	cmd := &cobra.Command{Use: "plan APP --database NAME --point ID --prepared-point ID --recovery-release ID --target NAME", Short: "Review selected data, current exact coverage and compatible application code", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		request.App = args[0]
		raw, err := json.Marshal(request)
		if err != nil {
			return err
		}
		if _, err = liverestore.DecodePlanRequest(raw); err != nil || !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "native_live_restore_plan", request)
		if err != nil {
			return err
		}
		p, ok := response.Data.(liverestore.Plan)
		if !ok || !response.OK || p.Spec().Facts.App != request.App || p.Spec().Safety.PointID != request.CurrentPreparedPointID || p.Spec().Selected.PointID != request.SelectedPointID || p.Spec().Recovery.ReleaseID != request.RecoveryReleaseID {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), struct {
				PlanID string           `json:"plan_id"`
				Plan   liverestore.Plan `json:"plan"`
			}{PlanID: p.ID(), Plan: p}))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Restore plan %s. Selected point %s. Current safety point %s. Possible loss: %s. Expires %s.\n", p.ID(), p.Spec().Selected.PointID, p.Spec().Safety.PointID, p.Loss().Kind, p.Spec().ExpiresAt.Format("2006-01-02T15:04:05Z"))
		return err
	}}
	flags.register(cmd)
	cmd.Flags().StringVar(&request.Database, "database", "", "Managed database name")
	cmd.Flags().StringVar(&request.SelectedPointID, "point", "", "Exact verified point to restore")
	cmd.Flags().StringVar(&request.CurrentPreparedPointID, "prepared-point", "", "Fresh exact point protecting the current source")
	cmd.Flags().StringVar(&request.RecoveryReleaseID, "recovery-release", "", "Compatible release whose installed code is independently checked")
	return cmd
}

func newNativeLiveRestoreApplyCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags nativeTaskFlags
	var planID string
	cmd := &cobra.Command{Use: "apply APP --plan ID --target NAME", Short: "Apply a reviewed restore and leave its new epoch stopped", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dispatch.ValidApp(args[0]) {
			return result.New(result.InvalidUsage, nil)
		}
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		request := liverestore.ApplyRequest{App: args[0], PlanID: planID, IdempotencyKey: flags.key}
		raw, err := json.Marshal(request)
		if err != nil {
			return err
		}
		if _, err = liverestore.DecodeApplyRequest(raw); err != nil {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.operation.call(cmd.Context(), deps, "native_live_restore", request)
		if err != nil {
			return err
		}
		accepted, ok := response.Data.(jobs.Accepted)
		if !response.OK || !ok || accepted.Status != "accepted" || !jobs.ValidID(accepted.OperationID) {
			return result.New(result.TransportInvalidResponse, nil)
		}
		value, err := flags.operation.waitTask(cmd.Context(), deps, accepted, ops.NativeLiveRestore, flags.noWait)
		if err != nil {
			return err
		}
		if flags.noWait {
			if modes.enabled() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Accepted operation %s. Check with %s.\n", accepted.OperationID, flags.operation.command("status", "--operation", accepted.OperationID))
			return err
		}
		receipt, ok := value.(liverestore.Receipt)
		if !ok || receipt.PlanID != planID || receipt.OperationID != accepted.OperationID {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), receipt))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Restored point %s into new epoch %s. Engine and application remain stopped.\n", receipt.SelectedPointID, receipt.EpochID)
		return err
	}}
	flags.register(cmd)
	cmd.Flags().StringVar(&planID, "plan", "", "Reviewed restore plan ID")
	return cmd
}

func newNativeLiveRestoreInspectCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	cmd := &cobra.Command{Use: "inspect OPERATION_ID --target NAME", Short: "Read the retained restore stage without retrying effects", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		request := liverestore.InspectRequest{OperationID: args[0]}
		raw, err := json.Marshal(request)
		if err != nil {
			return err
		}
		if _, err = liverestore.DecodeInspectRequest(raw); err != nil || !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "native_live_restore_inspect", request)
		if err != nil {
			return err
		}
		progress, ok := response.Data.(liverestore.Progress)
		if !ok || !response.OK || progress.OperationID != request.OperationID {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), progress))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Restore %s: %s, step %d.\n", progress.OperationID, progress.Stage, progress.Sequence)
		return err
	}}
	flags.register(cmd)
	return cmd
}

func newNativeRestoreResolutionReviewCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	cmd := &cobra.Command{Use: "review-resolution OPERATION_ID --target NAME", Short: "Review the actual retained layout of an interrupted restore", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		request := liverestore.InspectRequest{OperationID: args[0]}
		raw, _ := json.Marshal(request)
		if _, err := liverestore.DecodeInspectRequest(raw); err != nil || !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "native_live_restore_review_resolution", request)
		if err != nil {
			return err
		}
		review, ok := response.Data.(liverestore.Resolution)
		if !ok || !response.OK || review.OriginalOperationID != request.OperationID || review.ID() == "" {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), review))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Resolution review %s for %s. Expires %s.\n", review.ID(), review.OriginalOperationID, review.ExpiresAt.Format("2006-01-02T15:04:05Z"))
		return err
	}}
	flags.register(cmd)
	return cmd
}

func newNativeRestoreResolveCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags nativeTaskFlags
	var reviewID string
	cmd := &cobra.Command{Use: "resolve OPERATION_ID --review ID --target NAME", Short: "Complete a reviewed known restore effect without repeating extraction or initialization", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		request := liverestore.ResolveRequest{OriginalOperationID: args[0], ReviewedResolutionPlanID: reviewID, IdempotencyKey: flags.key}
		raw, _ := json.Marshal(request)
		if _, err := liverestore.DecodeResolveRequest(raw); err != nil {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.operation.call(cmd.Context(), deps, "native_live_restore_resolve", request)
		if err != nil {
			return err
		}
		accepted, ok := response.Data.(jobs.Accepted)
		if !response.OK || !ok || accepted.Status != "accepted" || !jobs.ValidID(accepted.OperationID) {
			return result.New(result.TransportInvalidResponse, nil)
		}
		value, err := flags.operation.waitTask(cmd.Context(), deps, accepted, ops.NativeLiveRestoreResolve, flags.noWait)
		if err != nil {
			return err
		}
		if flags.noWait {
			if modes.enabled() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Accepted resolution %s.\n", accepted.OperationID)
			return err
		}
		receipt, ok := value.(liverestore.Receipt)
		if !ok || receipt.PlanID != reviewID || receipt.OriginalOperationID != request.OriginalOperationID || receipt.OperationID != accepted.OperationID {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), receipt))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Restore resolved into epoch %s. Engine and application remain stopped.\n", receipt.EpochID)
		return err
	}}
	flags.register(cmd)
	cmd.Flags().StringVar(&reviewID, "review", "", "Fresh retained resolution review ID")
	return cmd
}
