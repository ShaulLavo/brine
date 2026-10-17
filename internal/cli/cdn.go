package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/cdnoperator"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

type cdnFlags struct {
	operationFlags
	profiles string
}

func (f *cdnFlags) register(cmd *cobra.Command) {
	f.operationFlags.register(cmd)
	cmd.Flags().StringVar(&f.profiles, "profiles", "", "Private operator CDN profile registry")
}

func (f cdnFlags) coordinator(deps Dependencies, providers bool) (cdnoperator.Coordinator, error) {
	c := cdnoperator.Coordinator{Host: remoteCDNHost{selection: f.operationFlags, deps: deps}}
	if !f.validSelection() {
		return c, result.New(result.InvalidUsage, nil)
	}
	if !providers {
		return c, nil
	}
	path := f.profiles
	if path == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return c, err
		}
		path = filepath.Join(base, "brine", "cdn-profiles.json")
	}
	registry, err := cdnoperator.LoadRegistry(path)
	if err != nil {
		return c, result.New(result.DependencyMissing, err)
	}
	c.Drivers = registry
	return c, nil
}

func newCDNCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	group := &cobra.Command{Use: "cdn", Short: "Manage host-journaled immutable-origin CDN attachments"}
	group.AddCommand(newCDNPlanCmd(deps, modes), newCDNApplyCmd(deps, modes), newCDNResumeCmd(deps, modes), newCDNStatusCmd(deps, modes))
	return group
}

func newCDNPlanCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags cdnFlags
	request := cdnoperator.PlanRequest{Mode: cdn.Enabled}
	var profile, mode, revision, recoverOperation string
	cmd := &cobra.Command{Use: "plan APP --binding NAME --profile NAME --sample PATH --target NAME", Short: "Review a connected CDN enable, switch, direct-origin or retained revision plan", Args: cobra.ExactArgs(1)}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		request.App, request.Profile, request.Mode = args[0], cdn.ProfileRef(profile), cdn.Mode(mode)
		request.Revision, request.RecoverOperation = cdn.RevisionID(revision), cdn.OperationID(recoverOperation)
		if !request.Valid() {
			return result.New(result.InvalidUsage, nil)
		}
		coordinator, err := flags.coordinator(deps, true)
		if err != nil {
			return err
		}
		p, err := coordinator.Plan(cmd.Context(), request)
		if err != nil {
			return cdnCommandError(err)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), p))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "CDN plan %s: %s for %s using %s. Native writes: %d.\n", p.ID, p.Context.Attachment.Mode, p.Context.Attachment.Binding.Hostname, p.Context.Attachment.Profile.Reference, len(p.Steps))
		return err
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&request.Binding, "binding", "", "Protected host binding")
	cmd.Flags().StringVar(&profile, "profile", "", "Policy-approved private edge profile")
	cmd.Flags().StringVar(&mode, "mode", "enabled", "Desired mode: enabled or direct")
	cmd.Flags().StringVar(&revision, "revision", "", "Retained CDN revision to restore through a new plan")
	cmd.Flags().StringVar(&recoverOperation, "recover-operation", "", "Fully settled failed operation whose reservation this recovery plan replaces")
	cmd.Flags().StringVar(&request.SamplePath, "sample", "", "Small existing hash-named immutable object path for full-body qualification")
	return cmd
}

func newCDNApplyCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags cdnFlags
	var planID, key string
	cmd := &cobra.Command{Use: "apply --plan-id ID --idempotency-key KEY --target NAME", Short: "Admit one host operation and run its one-shot native effects", Args: cobra.NoArgs}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		request := cdnoperator.ApplyRequest{PlanID: cdn.PlanID(planID), IdempotencyKey: key}
		if !request.Valid() {
			return result.New(result.InvalidUsage, nil)
		}
		coordinator, err := flags.coordinator(deps, true)
		if err != nil {
			return err
		}
		progress, err := coordinator.Apply(cmd.Context(), request)
		return writeCDNProgress(cmd, modes, progress, err)
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&planID, "plan-id", "", "Reviewed host CDN plan")
	cmd.Flags().StringVar(&key, "idempotency-key", "", "Reuse this key after a lost admission reply")
	return cmd
}

func newCDNResumeCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags cdnFlags
	var operation string
	cmd := &cobra.Command{Use: "resume --operation ID --target NAME", Short: "Inspect issued effects and continue unissued CDN work", Args: cobra.NoArgs}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if !cdnoperator.ValidOperation(cdn.OperationID(operation)) {
			return result.New(result.InvalidUsage, nil)
		}
		coordinator, err := flags.coordinator(deps, true)
		if err != nil {
			return err
		}
		progress, err := coordinator.Resume(cmd.Context(), cdn.OperationID(operation))
		return writeCDNProgress(cmd, modes, progress, err)
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&operation, "operation", "", "Host CDN operation")
	return cmd
}

func newCDNStatusCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags cdnFlags
	var app, operation string
	var refresh bool
	cmd := &cobra.Command{Use: "status --app APP [--operation ID] --target NAME", Short: "Inspect host history; refresh reads providers and the public path", Args: cobra.NoArgs}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		request := cdnoperator.StatusRequest{App: app, Operation: cdn.OperationID(operation)}
		if !request.Valid() {
			return result.New(result.InvalidUsage, nil)
		}
		coordinator, err := flags.coordinator(deps, refresh)
		if err != nil {
			return err
		}
		status, err := coordinator.Status(cmd.Context(), request, refresh)
		if err != nil {
			return cdnCommandError(err)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), status))
		}
		return writeCDNStatus(cmd, status)
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&app, "app", "", "Enrolled app")
	cmd.Flags().StringVar(&operation, "operation", "", "Inspect a particular host CDN operation")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Perform fresh provider reads and full public GETs")
	return cmd
}

func writeCDNProgress(cmd *cobra.Command, modes *machineModes, progress cdnoperator.Progress, err error) error {
	if err != nil {
		if progress.Operation != "" {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "operation_id=%s\n", progress.Operation)
		}
		return cdnCommandError(err)
	}
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), progress))
	}
	state := "waiting for inspection"
	if work, ok := progress.Work.(cdn.Finished); ok && work.Succeeded {
		state = "qualified and committed"
	}
	if work, ok := progress.Work.(cdn.Waiting); ok {
		state = work.Code
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "CDN operation %s: %s.\n", progress.Operation, state)
	return err
}

func writeCDNStatus(cmd *cobra.Command, status cdnoperator.Status) error {
	if status.Pending != nil {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "CDN operation %s: %s.\n", status.Pending.Operation, status.Pending.State)
		return err
	}
	if status.Head != nil {
		a := status.Head.Attachment
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "CDN %s: %s using %s, revision %s.\n", a.Binding.Hostname, a.Mode, a.Profile.Reference, a.Revision)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "No CDN attachment recorded for %s.\n", status.App)
	return err
}

func cdnCommandError(err error) error {
	return dispatch.CDNError(err)
}

type remoteCDNHost struct {
	selection operationFlags
	deps      Dependencies
}

func cdnCall[T any](ctx context.Context, host remoteCDNHost, op string, input any) (T, error) {
	var zero T
	response, err := host.selection.call(ctx, host.deps, op, input)
	if err != nil {
		return zero, err
	}
	value, ok := response.Data.(T)
	if !response.OK || !ok {
		return zero, result.New(result.TransportInvalidResponse, nil)
	}
	return value, nil
}

func (h remoteCDNHost) Context(ctx context.Context, input cdnoperator.PlanRequest) (cdnoperator.Planning, error) {
	return cdnCall[cdnoperator.Planning](ctx, h, "cdn_context", input)
}
func (h remoteCDNHost) SavePlan(ctx context.Context, input cdn.Plan) (cdn.Plan, error) {
	return cdnCall[cdn.Plan](ctx, h, "cdn_plan", dispatch.CDNSavePlanArgs{Plan: input})
}
func (h remoteCDNHost) Apply(ctx context.Context, input cdnoperator.ApplyRequest) (cdnoperator.Progress, error) {
	return cdnCall[cdnoperator.Progress](ctx, h, "cdn_apply", input)
}
func (h remoteCDNHost) Next(ctx context.Context, input cdnoperator.OperationRequest) (cdnoperator.Progress, error) {
	return cdnCall[cdnoperator.Progress](ctx, h, "cdn_next", input)
}
func (h remoteCDNHost) Seal(ctx context.Context, input cdnoperator.SealRequest) (cdnoperator.Progress, error) {
	return cdnCall[cdnoperator.Progress](ctx, h, "cdn_seal", input)
}
func (h remoteCDNHost) RecordResult(ctx context.Context, input cdnoperator.ResultRequest) (cdnoperator.Progress, error) {
	return cdnCall[cdnoperator.Progress](ctx, h, "cdn_result", input)
}
func (h remoteCDNHost) RecordInspection(ctx context.Context, input cdnoperator.InspectionRequest) (cdnoperator.Progress, error) {
	return cdnCall[cdnoperator.Progress](ctx, h, "cdn_inspect", input)
}
func (h remoteCDNHost) Qualify(ctx context.Context, input cdnoperator.ProofRequest) (cdnoperator.Progress, error) {
	return cdnCall[cdnoperator.Progress](ctx, h, "cdn_proof", input)
}
func (h remoteCDNHost) Status(ctx context.Context, input cdnoperator.StatusRequest) (cdnoperator.Status, error) {
	return cdnCall[cdnoperator.Status](ctx, h, "cdn_status", input)
}
