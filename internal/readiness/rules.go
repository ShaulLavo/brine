package readiness

import (
	"strings"

	"github.com/ShaulLavo/brine/internal/target"
)

type rule struct {
	code, severity, message, next string
	pass                          Fact[bool]
}

func check[T any](f Fact[T], predicate func(T) bool) Fact[bool] {
	if f.Status == "known" && f.Value != nil {
		return Known(predicate(*f.Value))
	}
	if f.Status == "absent" {
		return Known(false)
	}
	return Unknown[bool]("observation_unknown")
}
func isTrue(f Fact[bool]) Fact[bool]  { return check(f, func(v bool) bool { return v }) }
func isFalse(f Fact[bool]) Fact[bool] { return check(f, func(v bool) bool { return !v }) }
func every(facts ...Fact[bool]) Fact[bool] {
	all := true
	for _, f := range facts {
		if f.Value != nil && !*f.Value {
			return Known(false)
		}
		all = all && f.Status == "known" && f.Value != nil
	}
	if !all {
		return Unknown[bool]("observation_unknown")
	}
	return Known(true)
}
func prefix(f Fact[string], want string) Fact[bool] {
	return check(f, func(v string) bool {
		v = strings.TrimPrefix(v, "v")
		return v == want || strings.HasPrefix(v, want+".") || strings.HasPrefix(v, want+"-") || strings.HasPrefix(v, want+"+")
	})
}
func rules(r Report) []rule {
	f := r.Facts
	platform := every(check(f.OS, func(v target.OS) bool { return v.ID == "debian" && v.Version == "13" }), check(f.Arch, func(v string) bool { return v == "amd64" || v == "arm64" }))
	runtime := every(prefix(f.Systemd, "257"), prefix(f.Podman, "5.4"), prefix(f.Caddy, "2.6"), check(f.Passt, func(v string) bool { return v != "" }))
	disk := Unknown[bool]("observation_unknown")
	if f.FreeDiskBytes.Value != nil && f.Policy.Value != nil && f.Policy.Value.State != "invalid" {
		disk = Known(*f.FreeDiskBytes.Value >= f.Policy.Value.MinimumFreeDiskBytes)
	}
	return []rule{
		{"os_unsupported", "error", "The target requires Debian 13 on amd64 or arm64.", "brine doctor --target NAME", platform},
		{"runtime_version_mismatch", "error", "A required runtime is absent or outside the D2 reference versions.", "brine doctor --target NAME", runtime},
		{"cgroup_unsupported", "error", "The target does not use cgroup v2.", "brine doctor --target NAME", isTrue(f.CgroupV2)},
		{"rootless_helper_missing", "error", "Rootless uidmap helpers are missing or not setuid executables.", "brine doctor --target NAME", isTrue(f.UIDMap)},
		{"user_session_missing", "error", "A required runner session prerequisite is missing.", "brine doctor --target NAME", every(isTrue(f.DBus), isTrue(f.Polkit), isTrue(f.Linger), isTrue(f.UserBus))},
		{"disk_below_reserve", "error", "Free disk space is below the protected policy reserve.", "brine doctor --target NAME", disk},
		{"policy_invalid", "error", "The protected operator policy is invalid.", "brine permissions show --target NAME", check(f.Policy, func(v Policy) bool { return v.State != "invalid" })},
		{"policy_denies_deploy", "error", "The protected policy allows no deployment registries or domains.", "brine permissions show --target NAME", check(f.Policy, func(v Policy) bool { return v.State != "denies_deploy" })},
		{"caddy_inactive", "error", "Caddy is not active.", "brine diagnose --target NAME", isTrue(f.CaddyActive)},
		{"caddy_admin_exposed", "error", "Caddy administration is not restricted to loopback.", "brine diagnose --target NAME", isTrue(f.AdminLoopback)},
		{"proxy_conflict", "error", "An unrelated listener occupies HTTP or HTTPS ingress.", "brine diagnose --target NAME", isFalse(f.ProxyConflict)},
		{"catch_all_site", "error", "A hostless Caddy site conflicts with routed deployments.", "brine diagnose --target NAME", isFalse(f.CatchAll)},
		{"control_store_missing", "error", "The control store is missing.", "brine doctor --target NAME", check(f.Control, func(v Control) bool { return v.State != "missing" })},
		{"control_store_unhealthy", "error", "The isolated control store failed its SQLite structural check.", "brine doctor --target NAME", controlHealthy(f.Control)},
		{"control_store_upgrade_required", "error", "The control store requires a schema upgrade.", "brine doctor --target NAME", check(f.Control, func(v Control) bool { return v.State != "schema_upgrade_required" })},
	}
}

func controlHealthy(f Fact[Control]) Fact[bool] {
	if f.Value == nil {
		return Unknown[bool]("observation_unknown")
	}
	c := *f.Value
	switch c.State {
	case "current":
		return isTrue(c.QuickCheck)
	case "schema_unsupported", "preview_unavailable":
		return Unknown[bool]("store_unavailable")
	default:
		return Known(true)
	}
}

func Evaluate(r *Report) {
	all := rules(*r)
	for _, registry := range r.Registries {
		all = append(all, rule{"registry_unreachable", "error", "A policy registry did not answer anonymous HTTPS /v2/ with 200 or 401.", "brine doctor --target NAME", isTrue(registry.Reachable)})
	}
	for _, domain := range r.Domains {
		all = append(all, domainRules(domain)...)
	}
	if r.Backups.Value != nil {
		for _, b := range *r.Backups.Value {
			all = append(all, rule{"backup_stale", "warning", "An existing database backup is unverified, overdue or explicitly degraded.", "brine backup status " + b.App + " --database " + b.Database + " --target NAME", every(check(b.State, func(string) bool { return true }), isFalse(b.Stale))})
		}
	}
	r.Ready = Ready
	r.Findings = []Finding{}
	seen := map[string]bool{}
	for _, rule := range all {
		evaluateRule(r, rule, seen)
	}
	if r.Ready == Ready && (r.Truncated || r.Backups.Status != "known" || r.Facts.CommittedDomains.Status != "known" || r.Facts.Control.Value != nil && r.Facts.Control.Value.State != "current") {
		r.Ready = UnknownReady
	}
}
func evaluateRule(r *Report, rule rule, seen map[string]bool) {
	if rule.pass.Value == nil {
		if r.Ready != NotReady {
			r.Ready = UnknownReady
		}
		return
	}
	if *rule.pass.Value {
		return
	}
	r.Ready = NotReady
	if seen[rule.code] {
		return
	}
	seen[rule.code] = true
	r.Findings = append(r.Findings, Finding{Code: rule.code, Severity: rule.severity, Message: rule.message, NextOperations: []string{rule.next}})
}
func domainRules(d Domain) []rule {
	dns := d.DNS
	resolved := check(dns, func(v string) bool { return v != "unresolved" })
	here := check(dns, func(v string) bool { return v != "resolves_elsewhere" })
	if dns.Value != nil && *dns.Value == "unknown_behind_nat" {
		resolved = Unknown[bool]("observation_unknown")
		here = resolved
	}
	tls := check(d.TLS, func(v string) bool { return v == "trusted" })
	if d.TLS.Status == "absent" {
		tls = Known(true)
	}
	return []rule{
		{"domain_unresolved", "error", "A policy domain has no host DNS A or AAAA answer.", "brine doctor --target NAME", resolved},
		{"domain_elsewhere", "error", "A policy domain resolves to another host.", "brine doctor --target NAME", here},
		{"tls_untrusted", "error", "A committed route lacks a reachable, hostname-verified local TLS endpoint.", "brine diagnose --target NAME", tls},
	}
}
