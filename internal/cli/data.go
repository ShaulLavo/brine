// Package cli presents the typed operation engines as terminal commands.
package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/spf13/cobra"
)

func newDataCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	root := &cobra.Command{Use: "data", Short: "Prepare persistent data without starting an application"}
	var flags operationFlags
	prepare := &cobra.Command{Use: "prepare <brine.toml> --target NAME", Short: "Plan approved allocation before credentials, schema initialization and deploy", Args: offlineSpecArg, RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := readOfflineInput(args[0], dispatch.RequestLimit/2)
		if err != nil {
			return err
		}
		if _, err = spec.Parse(raw); err != nil {
			return result.New(result.InvalidUsage, err)
		}
		response, err := flags.call(cmd.Context(), deps, "data_prepare_plan", dispatch.PlanArgs{Spec: string(raw)})
		if err != nil {
			return err
		}
		p, ok := response.Data.(dispatch.Planned)
		if !response.OK || !ok {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), p))
		}
		if _, err = fmt.Fprintf(cmd.OutOrStdout(), "Data preparation plan %s: %s\n", p.PlanID, p.Kind); err != nil {
			return err
		}
		if p.Kind == plan.Conflict {
			return printConflicts(cmd.OutOrStdout(), p.Conflicts)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Apply this plan before credentials and schema initialization. No writer or replica will start.\n")
		return err
	}}
	flags.register(prepare)
	root.AddCommand(prepare)
	return root
}
