// Package cli presents the Brine command and its deterministic machine API.
package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func newDataInitCmd(deps Dependencies, modes *machineModes, local bool) *cobra.Command {
	var flags operationFlags
	var planned bool
	var id string
	var r datainit.Request
	use := "init APP"
	if local {
		use = "data-init APP"
	}
	command := &cobra.Command{Use: use, Short: "Initialize never-started empty data with a reviewed schema and verified backup", Hidden: local, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r.App = args[0]
		if !dispatch.ValidApp(r.App) || planned && (id != "" || !datainit.ValidID(r.FirstReleasePlan) || !datainit.ValidID(r.Artifact)) || !planned && (!datainit.ValidID(id) || r.FirstReleasePlan != "" || r.Artifact != "") {
			return result.New(result.InvalidUsage, nil)
		}
		var value any
		var err error
		if local {
			if deps.HostDataInitialization == nil {
				return result.New(result.DependencyMissing, nil)
			}
			if planned {
				value, err = deps.HostDataInitialization.Plan(cmd.Context(), r)
			} else {
				value, err = deps.HostDataInitialization.Apply(cmd.Context(), r.App, id)
			}
			err = dispatch.DataInitializationFailure(err)
		} else {
			op := "data_init_apply"
			var request any = dispatch.DataInitApplyArgs{App: r.App, PlanID: id}
			if planned {
				op = "data_init_plan"
				request = dispatch.DataInitPlanArgs(r)
			}
			response, callErr := flags.call(cmd.Context(), deps, op, request)
			err = callErr
			value = response.Data
		}
		if err != nil {
			return err
		}
		if local || modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), value))
		}
		if p, ok := value.(datainit.Plan); ok {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Initialization plan %s. Apply with brine data init %s --plan-id %s --target %s. App code is not started.\n", p.ID, r.App, p.ID, flags.target)
			return err
		}
		op, ok := value.(datainit.Operation)
		if !ok {
			return result.New(result.TransportInvalidResponse, nil)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Initialization %s: %s. Replan deployment before starting the first compatible release.\n", op.ID, op.State)
		return err
	}}
	command.Flags().BoolVar(&planned, "plan", false, "Record immutable initialization intent without changing schema")
	command.Flags().StringVar(&id, "plan-id", "", "Apply or inspect this initialization plan; never replay an attempted mutation")
	command.Flags().StringVar(&r.FirstReleasePlan, "first-release-plan", "", "Exact retained deployment plan for the intended first release")
	command.Flags().StringVar(&r.Artifact, "artifact", "", "Digest of an operator-installed reviewed schema initializer")
	if !local {
		flags.register(command)
	}
	return command
}

type lazyDataInitialization struct{ runtime *invocationRuntime }

func (r lazyDataInitialization) service(ctx context.Context) (dispatch.DataInitializationOperations, error) {
	if err := r.runtime.initialize(ctx, false); err != nil {
		return nil, err
	}
	if r.runtime.services.DataInitialization == nil {
		return nil, result.New(result.DependencyMissing, nil)
	}
	return r.runtime.services.DataInitialization, nil
}
func (r lazyDataInitialization) Plan(ctx context.Context, request datainit.Request) (datainit.Plan, error) {
	s, err := r.service(ctx)
	if err != nil {
		return datainit.Plan{}, err
	}
	return s.Plan(ctx, request)
}
func (r lazyDataInitialization) Apply(ctx context.Context, app, id string) (datainit.Operation, error) {
	s, err := r.service(ctx)
	if err != nil {
		return datainit.Operation{}, err
	}
	return s.Apply(ctx, app, id)
}

func HostDataInitializationRequested(ctx context.Context, args []string) bool {
	root := NewRootCommand(Dependencies{Context: ctx}) //nolint:contextcheck // Metadata-only Find never invokes host serve or its context-carrying command closure.
	command, _, err := root.Find(args)
	return err == nil && command != nil && command.CommandPath() == "brine host data-init"
}
