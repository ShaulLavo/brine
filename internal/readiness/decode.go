package readiness

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"slices"

	"github.com/ShaulLavo/brine/internal/cdnoperator"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
)

var errReport = errors.New("invalid readiness report")
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+~:-]{0,127}$`)

func DecodeReport(raw []byte) (Report, error) {
	var r Report
	bad := func() (Report, error) { return Report{}, result.New(result.TransportInvalidResponse, nil) }
	if len(raw) > MaxReportBytes || cdnoperator.DecodeExact(raw, &r) != nil || r.SchemaVersion != 1 || !validFacts(r.Facts) || r.Registries == nil || r.Domains == nil || r.Findings == nil || len(r.Registries) > MaxItems || len(r.Domains) > MaxItems || !validBackups(r.Backups) {
		return bad()
	}
	for i, registry := range r.Registries {
		if !validRegistryHost(registry.Host) || !validFact(registry.Reachable) || i > 0 && r.Registries[i-1].Host >= registry.Host {
			return bad()
		}
	}
	for i, domain := range r.Domains {
		if !validDomainFact(domain) || i > 0 && r.Domains[i-1].Name >= domain.Name {
			return bad()
		}
	}
	expected := r
	Evaluate(&expected)
	before, _ := json.Marshal(r.Findings)
	after, _ := json.Marshal(expected.Findings)
	if r.Ready != expected.Ready || !bytes.Equal(before, after) {
		return bad()
	}
	return r, nil
}
func validFact[T any](f Fact[T]) bool {
	if f.Status == "known" {
		return f.Value != nil && f.Reason == ""
	}
	if f.Status == "absent" {
		return f.Value == nil && slices.Contains([]string{"inventory_absent", "no_committed_route"}, f.Reason)
	}
	if f.Status != "unknown" || f.Value != nil {
		return false
	}
	return slices.Contains([]string{"inventory_unknown", "inventory_absent", "inventory_unavailable", "probe_unavailable", "probe_failed", "probe_timeout", "policy_unavailable", "store_unavailable", "observation_unknown", "route_unknown", "no_committed_route"}, f.Reason)
}
func validValue[T any](f Fact[T], validate func(T) bool) bool {
	return validFact(f) && (f.Value == nil || validate(*f.Value))
}
func enum(f Fact[string], values ...string) bool {
	return validValue(f, func(v string) bool { return slices.Contains(values, v) })
}
func validFacts(f Facts) bool {
	if !validValue(f.CommittedDomains, validDomains) {
		return false
	}
	if !validValue(f.OS, func(v target.OS) bool { return tokenPattern.MatchString(v.ID) && tokenPattern.MatchString(v.Version) }) || !validValue(f.Arch, tokenPattern.MatchString) {
		return false
	}
	for _, version := range []Fact[string]{f.Systemd, f.Podman, f.Passt, f.Caddy} {
		if !validValue(version, tokenPattern.MatchString) {
			return false
		}
	}
	for _, flag := range []Fact[bool]{f.CgroupV2, f.UIDMap, f.DBus, f.Polkit, f.Linger, f.UserBus, f.CaddyActive, f.AdminLoopback, f.ProxyConflict, f.CatchAll} {
		if !validFact(flag) {
			return false
		}
	}
	if !validFact(f.FreeDiskBytes) || !validValue(f.Policy, func(v Policy) bool { return slices.Contains([]string{"valid", "invalid", "denies_deploy"}, v.State) }) {
		return false
	}
	return validValue(f.Control, func(c Control) bool {
		return slices.Contains([]string{"missing", "current", "schema_upgrade_required", "schema_unsupported", "preview_unavailable"}, c.State) && validFact(c.QuickCheck) && (c.State == "current" || c.QuickCheck.Value == nil)
	})
}
func validDomainFact(d Domain) bool {
	return validExactDomain(d.Name) && enum(d.DNS, "resolves_here", "resolves_elsewhere", "unresolved", "unknown_behind_nat") && enum(d.TLS, "trusted", "untrusted", "unreachable") && (d.TLS.Status != "absent" || d.TLS.Reason == "no_committed_route")
}
func validBackups(f Fact[[]Backup]) bool {
	if !validFact(f) {
		return false
	}
	if f.Value == nil {
		return true
	}
	if *f.Value == nil || len(*f.Value) > MaxItems {
		return false
	}
	seen := map[string]bool{}
	for _, b := range *f.Value {
		key := b.App + "/" + b.Database
		if !namePattern.MatchString(b.App) || !namePattern.MatchString(b.Database) || seen[key] || !slices.Contains([]string{"sqlite", "spacetimedb"}, b.Engine) || !enum(b.State, "inactive", "recovery_required", "unverified", "verified", "degraded", "observed") || !validFact(b.Stale) {
			return false
		}
		seen[key] = true
	}
	return true
}

func validDomains(domains []string) bool {
	if domains == nil || len(domains) > MaxItems {
		return false
	}
	for i, name := range domains {
		if !validExactDomain(name) || i > 0 && domains[i-1] >= name {
			return false
		}
	}
	return true
}
