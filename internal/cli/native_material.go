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

func newNativeMaterialCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	group := &cobra.Command{Use: "material", Short: "Deliver immutable native publisher, signing and authentication material"}
	var selection operationFlags
	var key, planID string
	set := &cobra.Command{Use: "set APP --preparation-plan ID --target NAME", Short: "Deliver a private four-role JSON packet on stdin; leave native and frontend services stopped", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dispatch.ValidApp(args[0]) || !jobs.ValidPlanID(planID) || !selection.validSelection() || deps.Stdin == nil || isTerminal(deps.Stdin) || cmd.Flags().Changed("idempotency-key") && key == "" || key != "" && !data.ValidID(key) {
			return result.New(result.InvalidUsage, nil)
		}
		if key == "" {
			generated, err := randomOperationKey()
			if err != nil {
				return err
			}
			key = generated
			if _, err = fmt.Fprintf(cmd.ErrOrStderr(), "idempotency_key=%s\n", key); err != nil {
				return err
			}
		}
		packet, err := readSecretInput(cmd.Context(), deps.Stdin)
		defer clear(packet)
		if err != nil || len(packet) > nativematerial.PacketLimit {
			return result.New(result.InvalidUsage, nil)
		}
		wire, err := dispatch.EncodeNativeMaterial(args[0], planID, key, packet)
		defer clear(wire)
		if err != nil {
			return err
		}
		response, err := selection.call(cmd.Context(), deps, "native_material_set", wire)
		if err != nil {
			return err
		}
		receipt, ok := response.Data.(nativematerial.Receipt)
		if !response.OK || !ok || !receipt.Valid() || receipt.App != args[0] || receipt.PreparationPlanID != planID {
			return result.New(result.TransportInvalidResponse, nil)
		}
		return writeNativeMaterialResult(cmd, modes, receipt)
	}}
	selection.register(set)
	set.Flags().StringVar(&key, "idempotency-key", "", "Reuse this key to inspect the same immutable delivery after a lost response")
	set.Flags().StringVar(&planID, "preparation-plan", "", "Saved managed preparation plan (required)")
	var statusSelection operationFlags
	status := &cobra.Command{Use: "status OPERATION_ID --target NAME", Short: "Inspect retained delivery metadata without changing files or runtime", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !jobs.ValidID(args[0]) || !statusSelection.validSelection() {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := statusSelection.call(cmd.Context(), deps, "native_material_status", dispatch.NativeMaterialInspectArgs{OperationID: args[0]})
		if err != nil {
			return err
		}
		receipt, ok := response.Data.(nativematerial.Receipt)
		if !response.OK || !ok || !receipt.Valid() || receipt.OperationID != args[0] {
			return result.New(result.TransportInvalidResponse, nil)
		}
		return writeNativeMaterialResult(cmd, modes, receipt)
	}}
	statusSelection.register(status)
	group.AddCommand(set, status)
	return group
}
func writeNativeMaterialResult(cmd *cobra.Command, modes *machineModes, r nativematerial.Receipt) error {
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), r))
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Native material %s. Operation %s; publisher version %d.\n", r.Outcome, r.OperationID, r.Secrets.Publisher.Version)
	if err != nil || r.Outcome != "recovery_required" {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "Delivery needs inspection. Retained files and versions have not been replaced; no service was started.")
	return err
}
