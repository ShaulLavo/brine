package diagnose

import (
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"strings"
)

type rule struct {
	code, severity, message string
	next                    []string
	matches                 func(Host, App) bool
}

var hostRules = []rule{
	{"disk_below_minimum", "error", "Free disk is below the operator policy minimum. Free space before deploying.", []string{"brine diagnose --target NAME"}, func(h Host, _ App) bool {
		return h.FreeDiskBytes.Value != nil && h.MinimumFreeDiskBytes.Value != nil && *h.FreeDiskBytes.Value < *h.MinimumFreeDiskBytes.Value
	}},
	{"runner_linger_disabled", "error", "The runner's user services cannot survive logout because linger is disabled.", []string{"brine doctor", "brine enroll --help"}, func(h Host, _ App) bool { return h.Linger.Value != nil && !*h.Linger.Value }},
}
var appRules = []rule{
	{"unit_journal_unavailable", "warning", "Unit journal logs are unavailable to the runner. Use app logs for container output; ask the operator to inspect unit logs without widening deploy credentials.", []string{"brine logs APP --target NAME"}, func(_ Host, a App) bool {
		return a.UnitLogs.Status == "unknown" && a.UnitLogs.Reason == string(result.LogsJournalUnavailable)
	}},
	{"unit_failed", "error", "The app unit has failed. Read its logs before choosing an application rollback; rollback does not rewind data.", []string{"brine logs APP --target NAME", "brine rollback APP --target NAME"}, func(_ Host, a App) bool { return a.Unit.Value != nil && a.Unit.Value.ActiveState == "failed" }},
	{"unit_restarting", "warning", "The unit reports at least three automatic restarts. This counter is not a count since the last deploy.", []string{"brine logs APP --target NAME", "brine rollback APP --target NAME"}, func(_ Host, a App) bool { return a.Unit.Value != nil && a.Unit.Value.Restarts >= 3 }},
	{"container_stopped", "error", "The app container is not running. Inspect logs before changing it.", []string{"brine logs APP --target NAME"}, func(_ Host, a App) bool { return a.ContainerRunning.Value != nil && !*a.ContainerRunning.Value }},
	{"health_failed", "error", "The direct health check did not return the committed expected status.", []string{"brine logs APP --target NAME"}, func(_ Host, a App) bool { return a.Health.Value != nil && !*a.Health.Value }},
	{"route_missing", "error", "No live Caddy route was observed for this app.", []string{"brine plan --help"}, func(_ Host, a App) bool { return a.RoutePresent.Value != nil && !*a.RoutePresent.Value }},
	{"artifact_drift", "warning", "Observed app artifacts differ from the committed release. Replan before making changes.", []string{"brine plan --help"}, func(_ Host, a App) bool { return a.Drift.Value != nil && len(*a.Drift.Value) > 0 }},
	{"operation_failed", "warning", "The most recent app operation failed. Inspect its status and logs before retrying.", []string{"brine status --operation ID --target NAME", "brine logs APP --target NAME"}, func(_ Host, a App) bool { return latest(a, ops.Failed) }},
	{"recovery_required", "error", "The most recent operation needs recovery. Inspect its recorded state; do not blindly retry.", []string{"brine status --operation ID --target NAME"}, func(_ Host, a App) bool { return latest(a, ops.RecoveryRequired) || latest(a, ops.LaunchUnknown) }},
	{"plan_stale", "warning", "The most recent operation refused a stale plan. Make a fresh plan before applying.", []string{"brine plan --help"}, func(_ Host, a App) bool {
		return a.Operations.Value != nil && len(*a.Operations.Value) > 0 && (*a.Operations.Value)[0].FailureCode == "stale_plan"
	}},
}

func latest(a App, s ops.State) bool {
	return a.Operations.Value != nil && len(*a.Operations.Value) > 0 && (*a.Operations.Value)[0].State == s
}
func Findings(r Report) []Finding {
	out := []Finding{}
	add := func(rule rule, a App) {
		if rule.matches(r.Host, a) {
			next := make([]string, len(rule.next))
			copy(next, rule.next)
			for i, command := range next {
				command = strings.ReplaceAll(command, "APP", a.Name)
				if a.Operations.Value != nil && len(*a.Operations.Value) > 0 {
					command = strings.ReplaceAll(command, "ID", (*a.Operations.Value)[0].ID)
				}
				next[i] = command
			}
			out = append(out, Finding{Code: rule.code, Severity: rule.severity, App: a.Name, Message: rule.message, NextOperations: next})
		}
	}
	for _, rule := range hostRules {
		add(rule, App{})
	}
	for _, a := range r.Apps {
		for _, rule := range appRules {
			add(rule, a)
		}
	}
	return out
}
