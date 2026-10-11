package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

// Managed deployment is separate from the SQLite initialize-and-release flow.
// Its release plan is the only admissible input; a preview never starts work.
func deployManaged(cmd *cobra.Command, deps Dependencies, flags operationFlags, modes machineModes, request dispatch.PlanArgs, yes bool) error {
	response, err := flags.call(cmd.Context(), deps, "plan", request)
	if err != nil {
		return err
	}
	planned, ok := response.Data.(dispatch.Planned)
	if !ok || !response.OK {
		return result.New(result.TransportInvalidResponse, nil)
	}
	if modes.enabled() {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), planned)); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Managed plan %s: %s\n", planned.PlanID, planned.Kind); err != nil {
		return err
	}
	if planned.Kind == plan.Conflict {
		return result.New(result.Conflict, nil)
	}
	if !yes || planned.Kind == plan.NoOp {
		return nil
	}
	// The executable managed plan/receipt branch is installed with the managed
	// operation adapter. Its absence cannot fall through to SQLite effects.
	return result.New(result.Conflict, nil)
}
