package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newNativeMaterialPlanCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	var delivery string
	cmd := &cobra.Command{Use: "plan APP --delivery OPERATION_ID --target NAME", Short: "Review stored material activation after a planned stop; restart with the new signing versions", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dispatch.ValidApp(args[0]) || !jobs.ValidID(delivery) || !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "native_material_plan", dispatch.NativeMaterialPlanArgs{App: args[0], DeliveryOperationID: delivery})
		if err != nil {
			return err
		}
		p, ok := response.Data.(nativematerial.ActivationPlan)
		if !response.OK || !ok || !p.Valid() || p.App != args[0] || p.DeliveryOperationID != delivery {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), p))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Material plan %s selects publisher version %d. Signing reload required: %t. Activate with %s, then review and apply start.\n", p.ID, p.Secrets.Publisher.Version, p.RestartRequired, flags.command("data", "material", "activate", p.ID))
		return err
	}}
	flags.register(cmd)
	cmd.Flags().StringVar(&delivery, "delivery", "", "Successful stored_unreferenced delivery operation (required)")
	return cmd
}
func newNativeMaterialActivateCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	cmd := &cobra.Command{Use: "activate PLAN_ID --target NAME", Short: "Apply a reviewed material selection while all components remain stopped", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !data.ValidID(args[0]) || !flags.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "native_material_activate", dispatch.NativeMaterialActivateArgs{PlanID: args[0]})
		if err != nil {
			return err
		}
		r, ok := response.Data.(nativematerial.ActivationReceipt)
		if !response.OK || !ok || !r.Valid() || r.PlanID != args[0] {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), r))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Material is active at runtime version %d. Review and apply start to resume the app.\n", r.RuntimeVersion)
		return err
	}}
	flags.register(cmd)
	return cmd
}
