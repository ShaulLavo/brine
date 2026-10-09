package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func appStatus(cmd *cobra.Command, deps Dependencies, flags operationFlags, modes machineModes, args []string) error {
	app := ""
	if len(args) > 0 {
		app = args[0]
		if !dispatch.ValidApp(app) {
			return result.New(result.InvalidUsage, nil)
		}
	}
	response, err := flags.call(cmd.Context(), deps, "status", dispatch.AppStatusArgs{App: app})
	if err != nil {
		return err
	}
	report, ok := response.Data.(apps.Report)
	if !response.OK || !ok {
		return result.New(result.TransportInvalidResponse, nil)
	}
	if app != "" && (len(report.Apps) != 1 || report.Apps[0].App != app) {
		return result.New(result.TransportInvalidResponse, nil)
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	if modes.jsonl {
		for _, a := range report.Apps {
			if err := encoder.Encode(result.Success(cmd.CommandPath(), struct {
				Event string      `json:"event"`
				App   apps.Status `json:"app"`
			}{"app", a})); err != nil {
				return err
			}
		}
		return encoder.Encode(result.Success(cmd.CommandPath(), struct {
			Event string `json:"event"`
			Count int    `json:"count"`
		}{"complete", len(report.Apps)}))
	}
	if modes.json {
		return encoder.Encode(result.Success(cmd.CommandPath(), report))
	}
	if len(report.Apps) == 0 {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "No committed Brine apps.")
		return err
	}
	for _, a := range report.Apps {
		active := "not checked"
		if a.Health.UnitActive.Value != nil {
			if *a.Health.UnitActive.Value {
				active = "active"
			} else {
				active = "inactive"
			}
		}
		previous := "none"
		if a.Previous != nil {
			previous = releaseText(*a.Previous)
		}
		op := "none"
		if a.LastOperation != nil {
			op = a.LastOperation.ID + " (" + string(a.LastOperation.State) + ")"
		}
		direct := strings.ReplaceAll(a.Health.Direct, "_", " ")
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\n  Current: %s\n  Previous known-good: %s\n  Last operation: %s\n  Unit: %s; direct health: %s\n  Drift: %s%s\n", a.App, releaseText(a.Current), previous, op, active, direct, a.Drift.State, driftText(a.Drift.Fields)); err != nil {
			return err
		}
	}
	return nil
}
func releaseText(r apps.Release) string {
	digest := r.ImageDigest
	if len(digest) > 19 {
		digest = digest[:19]
	}
	domains := make([]string, len(r.Domains))
	for i, d := range r.Domains {
		domains[i] = string(d)
	}
	return fmt.Sprintf("%s, plan %s, image %s, port %d, domains %s", r.ID, r.PlanID, digest, r.Port, strings.Join(domains, ", "))
}
func driftText(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	return " (" + strings.Join(fields, ", ") + ")"
}
func newRollbackCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var flags operationFlags
	var release string
	cmd := &cobra.Command{Use: "rollback APP --target NAME", Short: "Plan a rollback without changing app or data", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !dispatch.ValidApp(args[0]) || release != "" && !dispatch.ValidRelease(release) || cmd.Flags().Changed("release") && release == "" {
			return result.New(result.InvalidUsage, nil)
		}
		response, err := flags.call(cmd.Context(), deps, "rollback", dispatch.RollbackArgs{App: args[0], ReleaseID: release})
		if err != nil {
			return err
		}
		p, ok := response.Data.(apps.RollbackPlan)
		if !response.OK || !ok {
			return result.New(result.TransportInvalidResponse, nil)
		}
		if modes.enabled() {
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), p)); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Rollback plan %s targets release %s (%s).\n", p.PlanID, p.ReleaseID, p.Compatibility); err != nil {
				return err
			}
			if p.Diff != nil {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Configuration diff (old -> new):"); err != nil {
					return err
				}
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(p.Diff); err != nil {
					return err
				}
			}
			if p.Kind == "conflict" {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "The plan has conflicts. Resolve them and plan again before applying.")
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Apply with brine apply %s --target %s. App rollback never rewinds data.\n", p.PlanID, flags.target); err != nil {
				return err
			}
		}

		return nil
	}}
	flags.register(cmd)
	cmd.Flags().StringVar(&release, "release", "", "Stored release ID (default: previous known-good)")
	return cmd
}
