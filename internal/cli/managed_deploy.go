package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

// Managed deployment uses the same detached apply job and never enters the
// SQLite credential/initialization workflow or uploads a module.
func deployManaged(cmd *cobra.Command, deps Dependencies, flags operationFlags, modes machineModes, request dispatch.PlanArgs, yes bool) error {
	response, err := flags.call(cmd.Context(), deps, "plan", request)
	if err != nil {
		return err
	}
	planned, ok := response.Data.(dispatch.Planned)
	if !ok || !response.OK {
		return result.New(result.TransportInvalidResponse, nil)
	}
	if planned.Kind == plan.Conflict || !yes || planned.Kind == plan.NoOp {
		if modes.enabled() {
			err = json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), planned))
		} else {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Managed plan %s: %s\n", planned.PlanID, planned.Kind)
		}
		if err != nil {
			return err
		}
		if planned.Kind == plan.Conflict {
			return result.New(result.Conflict, nil)
		}
		return nil
	}
	if planned.Managed == nil || planned.Managed.State != data.EngineUnpublished && planned.Managed.State != data.EngineInitialized {
		return result.New(result.Conflict, nil)
	}
	key, err := randomOperationKey()
	if err != nil {
		return err
	}
	response, err = flags.call(cmd.Context(), deps, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: key})
	if err != nil {
		return err
	}
	accepted, ok := response.Data.(jobs.Accepted)
	if !response.OK || !ok || accepted.Status != "accepted" || accepted.OperationID == "" {
		return result.New(result.TransportInvalidResponse, nil)
	}
	if err := flags.waitDeployment(cmd.Context(), deps, deploymentStep{Name: "release", PlanID: planned.PlanID, OperationID: accepted.OperationID}); err != nil {
		return err
	}
	status := "deployed"
	if planned.Managed.State == data.EngineUnpublished {
		status = "prepared"
	}
	completion := struct {
		PlanID      string `json:"plan_id"`
		OperationID string `json:"operation_id"`
		Status      string `json:"status"`
		Frontend    bool   `json:"frontend"`
	}{planned.PlanID, accepted.OperationID, status, planned.Managed.Frontend}
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), completion))
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Managed app %s. Operation %s.\n", status, accepted.OperationID)
	if err != nil {
		return err
	}
	if status == "prepared" {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "The database is unpublished; the engine, frontend, and client ingress have not started.")
	}
	return err
}
