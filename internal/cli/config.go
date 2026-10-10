package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/secrets"
	"github.com/spf13/cobra"
)

func newConfigCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	parent := &cobra.Command{Use: "config", Short: "Plan app configuration changes"}
	var flags operationFlags
	var unset []string
	cmd := &cobra.Command{Use: "set APP KEY=VALUE... --target NAME", Short: "Edit environment, resources, domains, health or secret references", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dispatch.ValidApp(args[0]) || len(args) == 1 && len(unset) == 0 {
			return result.New(result.InvalidUsage, nil)
		}
		edits := []apps.Edit{}
		for _, arg := range args[1:] {
			key, value, ok := strings.Cut(arg, "=")
			if !ok || key == "" {
				return result.New(result.InvalidUsage, nil)
			}
			action := ""
			switch key {
			case "domains.add":
				key, action = "domains", "add"
			case "domains.remove":
				key, action = "domains", "remove"
			}
			if !strings.Contains(key, ".") && key != "domains" {
				key = "environment." + key
			}
			edits = append(edits, apps.Edit{Key: key, Value: value, Action: action})
		}
		for _, key := range unset {
			if !strings.Contains(key, ".") {
				key = "environment." + key
			}
			if !strings.HasPrefix(key, "environment.") {
				return result.New(result.InvalidUsage, nil)
			}
			edits = append(edits, apps.Edit{Key: key, Action: "unset"})
		}
		response, err := flags.call(cmd.Context(), deps, "config_set", dispatch.ConfigArgs{App: args[0], Edits: edits})
		if err != nil {
			return err
		}
		return configOutput(cmd, response, *modes, flags)
	}}
	flags.register(cmd)
	cmd.Flags().StringArrayVar(&unset, "unset", nil, "Remove an environment key (repeatable)")
	parent.AddCommand(cmd)
	return parent
}
func newLifecycleCmd(deps Dependencies, modes *machineModes, verb string, action plan.ChangeKind) *cobra.Command {
	var flags operationFlags
	cmd := &cobra.Command{Use: verb + " APP --target NAME", Short: "Plan to " + verb + " the app's owned unit", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dispatch.ValidApp(args[0]) {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "lifecycle", dispatch.LifecycleArgs{App: args[0], Action: action})
		if err != nil {
			return err
		}
		return configOutput(cmd, response, *modes, flags)
	}}
	flags.register(cmd)
	return cmd
}
func configOutput(cmd *cobra.Command, response result.Envelope, modes machineModes, flags operationFlags) error {
	p, ok := response.Data.(apps.ConfigPlan)
	if !response.OK || !ok {
		return result.New(result.TransportInvalidResponse, nil)
	}
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), p))
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Plan %s (%s). No app changes have been applied.\n", p.PlanID, p.Kind); err != nil {
		return err
	}
	if p.Lifecycle == plan.RemoveApp {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Action: remove_app. Withdraw the route, stop the app, remove its unit and release its port. Retained releases and secret versions stay for D5 rollback history. Persistent data needs P04-08 archival."); err != nil {
			return err
		}
	} else if p.Lifecycle != "" {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Action: %s. Releases, routes and app data stay unchanged.\n", p.Lifecycle); err != nil {
			return err
		}
	}
	if p.Diff != nil {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(p.Diff); err != nil {
			return err
		}
	}
	if p.Kind == plan.Conflict {
		return printConflicts(cmd.OutOrStdout(), p.Conflicts)
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Apply with %s.\n", flags.command("apply", p.PlanID))
	return err
}
func newSecretCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	parent := &cobra.Command{Use: "secret", Short: "Store unbound app secret versions"}
	var flags operationFlags
	cmd := &cobra.Command{Use: "set APP NAME --target NAME", Short: "Read a secret value from stdin and store an immutable unbound version", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if !ops.ValidIntent(ops.Intent{Kind: ops.SecretSet, App: args[0], SecretRef: args[1]}) || !flags.validSelection() || deps.Stdin == nil {
			return result.New(result.InvalidUsage, nil)
		}
		input, err := readSecretInput(cmd.Context(), deps.Stdin)
		defer clear(input)
		if err != nil {
			return err
		}
		if len(input) == 0 || len(input) > secrets.ValueLimit {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "secret_set", dispatch.SecretArgs{App: args[0], Reference: args[1], Value: input})
		if err != nil {
			return err
		}
		stored, ok := response.Data.(secrets.Stored)
		if !response.OK || !ok || stored.Bound || !strings.HasPrefix(stored.VersionName, "brine."+args[0]+"."+args[1]+".v") {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), stored))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Stored %s in operation %s. It is unbound; a later config plan must bind the reference before apply.\n", stored.VersionName, stored.OperationID)
		return err
	}}
	flags.register(cmd)
	parent.AddCommand(cmd)
	return parent
}
