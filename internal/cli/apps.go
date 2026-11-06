package cli

import (
	"context"
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
	}
	report, err := readAppReport(cmd.Context(), deps, flags, app)
	if err != nil {
		return err
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
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), strings.Join(appStatusLines(a), "\n")); err != nil {
			return err
		}
	}
	return nil
}

func readAppReport(ctx context.Context, deps Dependencies, flags operationFlags, app string) (apps.Report, error) {
	if app != "" && !dispatch.ValidApp(app) {
		return apps.Report{}, result.New(result.InvalidUsage, nil)
	}
	response, err := flags.call(ctx, deps, "status", dispatch.AppStatusArgs{App: app})
	if err != nil {
		return apps.Report{}, err
	}
	report, ok := response.Data.(apps.Report)
	if !response.OK || !ok || app != "" && (len(report.Apps) != 1 || report.Apps[0].App != app) {
		return apps.Report{}, result.New(result.TransportInvalidResponse, nil)
	}
	return report, nil
}

func appStatusLines(a apps.Status) []string {
	lines := make([]string, 0, 8)
	if a.Managed != nil {
		lines = append(lines, fmt.Sprintf("%s: managed engine %s; prepared=%t, fenced=%t; engine logs unavailable", a.App, a.Managed.State, a.Managed.Prepared, a.Managed.Fenced))
	}
	active := "not checked"
	if a.Health.UnitActive.Value != nil {
		active = "inactive"
		if *a.Health.UnitActive.Value {
			active = "active"
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
	lines = append(lines, a.App,
		"  Current: "+currentReleaseText(a.Current),
		"  Previous known-good: "+previous,
		"  Last operation: "+op,
		"  Unit: "+active+"; direct health: "+direct,
		"  Drift: "+a.Drift.State+driftText(a.Drift.Fields))
	if a.UDP != nil {
		var udp strings.Builder
		// A strings.Builder cannot fail a write.
		_ = printUDPStatus(&udp, a)
		lines = append(lines, strings.TrimSuffix(udp.String(), "\n"))
	}
	return lines
}

func currentReleaseText(r *apps.Release) string {
	if r == nil {
		return "none"
	}
	return releaseText(*r)
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
		if cmd.Flags().Changed("release") && release == "" {
			return result.New(result.InvalidUsage, nil)
		}
		p, err := planRollback(cmd.Context(), deps, flags, args[0], release)
		if err != nil {
			return err
		}
		return rollbackOutput(cmd, p, *modes, flags)
	}}
	flags.register(cmd)
	cmd.Flags().StringVar(&release, "release", "", "Stored release ID (default: previous known-good)")
	return cmd
}

func rollbackOutput(cmd *cobra.Command, p apps.RollbackPlan, modes machineModes, flags operationFlags) error {
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), p))
	}
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
		return printConflicts(cmd.OutOrStdout(), p.Conflicts)
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Apply with %s. App rollback never rewinds data.\n", flags.command("apply", p.PlanID))
	return err
}

func planRollback(ctx context.Context, deps Dependencies, flags operationFlags, app, release string) (apps.RollbackPlan, error) {
	if !dispatch.ValidApp(app) || release != "" && !dispatch.ValidRelease(release) {
		return apps.RollbackPlan{}, result.New(result.InvalidUsage, nil)
	}
	response, err := flags.call(ctx, deps, "rollback", dispatch.RollbackArgs{App: app, ReleaseID: release})
	if err != nil {
		return apps.RollbackPlan{}, err
	}
	p, ok := response.Data.(apps.RollbackPlan)
	if !response.OK || !ok {
		return apps.RollbackPlan{}, result.New(result.TransportInvalidResponse, nil)
	}
	return p, nil
}
