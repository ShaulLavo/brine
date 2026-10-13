package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func publicArchiveResult(cmd *cobra.Command, modes *machineModes, value any, text string) error {
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), text)
	return err
}
func newPublicArchiveStatusCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	cmd := &cobra.Command{Use: "status ARCHIVE_ID --target NAME", Short: "Read archived credential health, retained verification and local deletion state", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) || !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "archive_status", dispatch.ArchiveMaintenanceStatusArgs{ArchiveID: data.ArchiveID(args[0])})
		if err != nil {
			return err
		}
		status, ok := response.Data.(archive.MaintenanceStatus)
		if !response.OK || !ok || string(status.ArchiveID) != args[0] {
			return result.New(result.TransportInvalidResponse, nil)
		}
		return publicArchiveResult(cmd, modes, status, fmt.Sprintf("Archive %s: %s. Retain until %s.", status.ArchiveID, status.State, status.RetainUntil.Format(time.RFC3339)))
	}}
	flags.register(cmd)
	return cmd
}
func newPublicArchiveMaintainCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	root := &cobra.Command{Use: "maintain", Short: "Review retained-data verification or local deletion and inspect interrupted deletion"}
	var flags operationFlags
	var kind string
	planCmd := &cobra.Command{Use: "plan ARCHIVE_ID --kind KIND --target NAME", Short: "Review exact verify_retention, purge_archive or expire_archive intent", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		k := archive.MaintenanceKind(kind)
		if !data.ValidID(args[0]) || !flags.validSelection() || k != archive.VerifyRetention && k != archive.PurgeArchive && k != archive.ExpireArchive {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "archive_maintenance_plan", dispatch.ArchiveMaintenancePlanArgs{ArchiveID: data.ArchiveID(args[0]), Kind: k})
		if err != nil {
			return err
		}
		p, ok := response.Data.(archive.MaintenancePlan)
		if !response.OK || !ok || !p.Valid() || string(p.ArchiveID) != args[0] || p.Kind != k {
			return result.New(result.TransportInvalidResponse, nil)
		}
		return publicArchiveResult(cmd, modes, p, fmt.Sprintf("Maintenance plan %s for archive %s: %s.", p.ID, p.ArchiveID, p.Kind))
	}}
	flags.register(planCmd)
	planCmd.Flags().StringVar(&kind, "kind", "", "verify_retention, purge_archive or expire_archive (required)")
	root.AddCommand(planCmd, newPublicArchiveMaintenanceApplyCmd(deps, modes, false), newPublicArchiveMaintenanceApplyCmd(deps, modes, true))
	return root
}
func newPublicArchiveMaintenanceApplyCmd(deps Dependencies, modes *machineModes, resolve bool) *cobra.Command {
	var flags nativeTaskFlags
	var planID string
	use, short := "apply ARCHIVE_ID --plan ID --target NAME", "Apply one exact reviewed maintenance plan in a detached operation"
	kind := ops.ArchiveMaintenanceApply
	if resolve {
		use, short = "resolve OPERATION_ID --target NAME", "Inspect and settle only an already-journaled unknown local deletion"
		kind = ops.ArchiveMaintenanceResolve
	}
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if resolve && !jobs.ValidID(args[0]) || !resolve && (!data.ValidID(args[0]) || !jobs.ValidPlanID(planID)) {
			return result.New(result.InvalidUsage, nil)
		}
		if err := flags.prepareKey(cmd); err != nil {
			return err
		}
		var input any = dispatch.ArchiveMaintenanceApplyArgs{ArchiveID: data.ArchiveID(args[0]), PlanID: planID, IdempotencyKey: flags.key}
		if resolve {
			input = dispatch.ArchiveMaintenanceResolveArgs{OperationID: args[0], IdempotencyKey: flags.key}
		}
		response, err := flags.operation.call(cmd.Context(), deps, string(kind), input)
		if err != nil {
			return err
		}
		accepted, ok := response.Data.(jobs.Accepted)
		if !response.OK || !ok || accepted.Status != "accepted" || !jobs.ValidID(accepted.OperationID) {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if err := announceArchiveMaintenanceOperation(cmd, accepted, flags.noWait); err != nil {
			return err
		}
		value, err := flags.operation.waitTask(cmd.Context(), deps, accepted, kind, flags.noWait)
		if err != nil {
			return err
		}
		if flags.noWait {
			return publicArchiveResult(cmd, modes, value, "Accepted archive maintenance operation "+accepted.OperationID+".")
		}
		receipt, ok := value.(archive.MaintenanceReceipt)
		if !ok || !receipt.Valid() || receipt.OperationID != accepted.OperationID || !resolve && (receipt.PlanID != planID || string(receipt.ArchiveID) != args[0]) || resolve && receipt.OriginalOperationID != args[0] {
			return result.New(result.TransportInvalidResponse, nil)
		}
		return publicArchiveResult(cmd, modes, receipt, fmt.Sprintf("Archive %s: %s. Remote recovery objects remain retained.", receipt.ArchiveID, receipt.Outcome))
	}}
	flags.register(cmd)
	if !resolve {
		cmd.Flags().StringVar(&planID, "plan", "", "Exact reviewed maintenance plan")
	}
	return cmd
}
func newPublicArchiveCredentialsCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	root := &cobra.Command{Use: "credentials", Short: "Renew only one retained archive binding and epoch without activating a replica"}
	root.AddCommand(newPublicArchiveCredentialCommand(deps, modes, true), newPublicArchiveCredentialCommand(deps, modes, false))
	return root
}
func newPublicArchiveCredentialCommand(deps Dependencies, modes *machineModes, planned bool) *cobra.Command {
	var flags operationFlags
	var binding, epoch, planID, expiryText string
	verb, short := "set", "Store bounded private S3 credential JSON on stdin in the archived namespace"
	if planned {
		verb, short = "plan", "Review archived credential renewal with current original binding/epoch policy"
	}
	cmd := &cobra.Command{Use: verb + " ARCHIVE_ID --binding ID --epoch ID --target NAME", Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) || !data.ValidID(binding) || !data.ValidID(epoch) || !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		if planned {
			return publicArchiveCredentialPlan(cmd, deps, modes, flags, args[0], binding, epoch, expiryText)
		}
		if !backupcredentials.ValidPlanID(planID) || deps.Stdin == nil || isTerminal(deps.Stdin) {
			return result.New(result.InvalidUsage, nil)
		}
		raw, err := readSecretInput(cmd.Context(), deps.Stdin)
		defer clear(raw)
		if err != nil || len(raw) > backupcredentials.PacketLimit {
			return result.New(result.InvalidUsage, nil)
		}
		wire, err := dispatch.EncodeArchiveCredentialSet(args[0], binding, epoch, planID, raw)
		defer clear(wire)
		if err != nil {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "archive_credentials_set", wire)
		if err != nil {
			return err
		}
		receipt, ok := response.Data.(backupcredentials.ArchiveReceipt)
		if !response.OK || !ok || !receipt.Valid() || receipt.Plan.ID != planID || receipt.Plan.Scope.ArchiveID != args[0] || receipt.Plan.Scope.Binding != binding || receipt.Plan.Scope.Epoch != epoch {
			return result.New(result.TransportInvalidResponse, nil)
		}
		return publicArchiveResult(cmd, modes, receipt, fmt.Sprintf("Stored archived credential version %d. No replica or application was activated.", receipt.Plan.Version))
	}}
	flags.register(cmd)
	cmd.Flags().StringVar(&binding, "binding", "", "Original archived binding ID (required)")
	cmd.Flags().StringVar(&epoch, "epoch", "", "Original archived epoch ID (required)")
	if planned {
		cmd.Flags().StringVar(&expiryText, "expires-at", "", "Optional issuer-supplied UTC expiry")
	} else {
		cmd.Flags().StringVar(&planID, "plan", "", "Exact reviewed archived credential plan")
	}
	return cmd
}

func publicArchiveCredentialPlan(cmd *cobra.Command, deps Dependencies, modes *machineModes, flags operationFlags, archiveID, binding, epoch, expiryText string) error {

	var expiry *time.Time
	if expiryText != "" {
		t, err := time.Parse(time.RFC3339Nano, expiryText)
		if err != nil || !strings.HasSuffix(expiryText, "Z") {
			return result.New(result.InvalidUsage, nil)
		}
		expiry = &t
	}
	response, err := flags.call(cmd.Context(), deps, "archive_credentials_plan", dispatch.ArchiveCredentialPlanArgs{ArchiveID: archiveID, Binding: binding, Epoch: epoch, ExpiresAt: expiry})
	if err != nil {
		return err
	}
	p, ok := response.Data.(backupcredentials.ArchivePlan)
	if !response.OK || !ok || !p.Valid() || p.Scope.ArchiveID != archiveID || p.Scope.Binding != binding || p.Scope.Epoch != epoch {
		return result.New(result.TransportInvalidResponse, nil)
	}
	return publicArchiveResult(cmd, modes, p, fmt.Sprintf("Archived credential plan %s, version %d. Delivery cannot activate a replica.", p.ID, p.Version))
}

func announceArchiveMaintenanceOperation(cmd *cobra.Command, accepted jobs.Accepted, noWait bool) error {
	if noWait {
		return nil
	}
	_, err := fmt.Fprintf(cmd.ErrOrStderr(), "operation_id=%s\n", accepted.OperationID)
	return err
}
