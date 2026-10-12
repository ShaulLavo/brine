package cli

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newModulePublicationCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	root := &cobra.Command{Use: "module", Short: "Review and publish a SpacetimeDB module against an exact prepared point"}
	root.AddCommand(newModulePlanCmd(deps, modes), newModuleApplyCmd(deps, modes, false), newModuleInspectCmd(deps, modes), newModuleApplyCmd(deps, modes, true))
	return root
}

func newModulePlanCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	var point, declaration, wasm string
	var releases []string
	cmd := &cobra.Command{Use: "plan APP --point POINT_ID --deployment-plan PLAN_ID --wasm FILE --target NAME", Short: "Review the owned module bytes, actual native schema and compatible recovery releases", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := readOfflineInput(wasm, datapublish.ModuleArtifactLimit)
		if err != nil {
			return err
		}
		if releases == nil {
			releases = []string{}
		}
		slices.Sort(releases)
		input := dispatch.ModulePlanArgs{App: args[0], PreparedPointID: point, TargetDeploymentPlanID: declaration, Wasm: raw, RecoveryReleaseIDs: releases}
		if !input.Valid() || !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "module_publish_plan", input)
		if err != nil {
			return err
		}
		view, ok := response.Data.(datapublish.ReviewView)
		if !response.OK || !ok || !view.Valid() || view.App != input.App || view.PointID != input.PreparedPointID {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), view))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Reviewed module plan %s for %s. Expires %s. Apply this reference to publish and activate data; then create a new deployment plan to start the app.\n", view.PlanID, view.App, view.ExpiresAt.Format("2006-01-02T15:04:05Z"))
		return err
	}}
	flags.register(cmd)
	cmd.Flags().StringVar(&point, "point", "", "Exact prepared point reference")
	cmd.Flags().StringVar(&declaration, "deployment-plan", "", "Immutable target deployment declaration reference")
	cmd.Flags().StringVar(&wasm, "wasm", "", "Local owned Wasm artifact")
	cmd.Flags().StringSliceVar(&releases, "recovery-release", nil, "Compatible retained release IDs; omit only when this incarnation has no release history")
	return cmd
}

func newModuleInspectCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	cmd := &cobra.Command{Use: "inspect PLAN_ID --target NAME", Short: "Inspect the retained publication attempt without changing its state", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		input := dispatch.ModuleInspectArgs{PlanID: args[0]}
		if !input.Valid() || !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "module_publish_inspect", input)
		if err != nil {
			return err
		}
		view, ok := response.Data.(datapublish.Inspection)
		if !response.OK || !ok || !view.Valid() || view.PlanID != input.PlanID {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), view))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Module plan %s: %s, step %s, transition %s.\n", view.PlanID, view.State, view.Step, view.TransitionState)
		return err
	}}
	flags.register(cmd)
	return cmd
}

func newModuleApplyCmd(deps Dependencies, modes *machineModes, resolving bool) *cobra.Command {
	var flags nativeTaskFlags
	var original string
	name, short, kind := "apply", "Publish and activate the reviewed module without starting the app", ops.ModulePublishApply
	if resolving {
		name, short, kind = "resolve", "Inspect and resolve the original publication without repeating native effects", ops.ModulePublishResolve
	}
	cmd := &cobra.Command{Use: name + " APP PLAN_ID --target NAME", Short: short, Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		var input any = dispatch.ModuleApplyArgs{App: args[0], PlanID: args[1], IdempotencyKey: flags.key}
		if resolving {
			request := dispatch.ModuleResolveArgs{App: args[0], PlanID: args[1], OriginalOperationID: original, IdempotencyKey: flags.key}
			if !request.Valid() {
				return result.New(result.InvalidUsage, nil)
			}
			input = request
		} else if !input.(dispatch.ModuleApplyArgs).Valid() {
			return result.New(result.InvalidUsage, nil)
		}
		return runModulePublicationTask(cmd, deps, modes, flags, kind, input, args[1])
	}}
	flags.register(cmd)
	if resolving {
		cmd.Flags().StringVar(&original, "operation", "", "Original publication apply operation reference")
	}
	return cmd
}

func runModulePublicationTask(cmd *cobra.Command, deps Dependencies, modes *machineModes, flags nativeTaskFlags, kind ops.Kind, input any, planID string) error {
	response, err := flags.operation.call(cmd.Context(), deps, string(kind), input)
	if err != nil {
		return err
	}
	accepted, ok := response.Data.(jobs.Accepted)
	if !response.OK || !ok || accepted.Status != "accepted" || !jobs.ValidID(accepted.OperationID) {
		return result.New(result.TransportInvalidResponse, nil)
	}
	if !flags.noWait {
		if _, err = fmt.Fprintf(cmd.ErrOrStderr(), "operation_id=%s\n", accepted.OperationID); err != nil {
			return err
		}
	}
	value, err := flags.operation.waitTask(cmd.Context(), deps, accepted, kind, flags.noWait)
	if err != nil {
		return err
	}
	if flags.noWait {
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Accepted operation %s.\n", accepted.OperationID)
		return err
	}
	receipt, ok := value.(datapublish.TaskReceipt)
	if !ok || receipt.PlanID != planID {
		return result.New(result.TransportInvalidResponse, nil)
	}
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), receipt))
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Module publication %s. Data epoch %s. Create a new deployment plan to start the app.\n", receipt.Outcome, receipt.NewEpochID)
	return err
}
