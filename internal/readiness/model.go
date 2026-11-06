// Package readiness composes bounded host observations without mutation authority.
package readiness

import (
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/target"
)

const TotalTimeout = 12 * time.Second
const ProbeTimeout = time.Second
const InventoryTimeout = 4 * time.Second
const MaxItems = 16
const MaxReportBytes = 128 << 10

type Fact[T any] struct {
	Status string `json:"status"`
	Value  *T     `json:"value"`
	Reason string `json:"reason"`
}
type Finding struct {
	Code           string   `json:"code"`
	Severity       string   `json:"severity"`
	Message        string   `json:"message"`
	NextOperations []string `json:"next_operations"`
}

func Known[T any](value T) Fact[T]         { return Fact[T]{Status: "known", Value: &value} }
func Unknown[T any](reason string) Fact[T] { return Fact[T]{Status: "unknown", Reason: reason} }
func observed[T any](o target.Observation[T]) Fact[T] {
	if o.Status == target.KnownStatus && o.Value != nil {
		return Known(*o.Value)
	}
	if o.Status == target.Absent {
		return Fact[T]{Status: "absent", Reason: "inventory_absent"}
	}
	return Unknown[T]("inventory_unknown")
}

type Verdict string

const Ready Verdict = "true"
const NotReady Verdict = "false"
const UnknownReady Verdict = "unknown"

func (v Verdict) MarshalJSON() ([]byte, error) {
	if v == Ready || v == NotReady {
		return []byte(v), nil
	}
	return json.Marshal(string(v))
}
func (v *Verdict) UnmarshalJSON(raw []byte) error {
	for _, allowed := range []Verdict{Ready, NotReady, UnknownReady} {
		encoded, _ := allowed.MarshalJSON()
		if string(encoded) == string(raw) {
			*v = allowed
			return nil
		}
	}
	return errReport
}

type Report struct {
	SchemaVersion int            `json:"schema_version"`
	Ready         Verdict        `json:"ready"`
	Facts         Facts          `json:"facts"`
	Registries    []Registry     `json:"registries"`
	Domains       []Domain       `json:"domains"`
	Backups       Fact[[]Backup] `json:"backups"`
	Truncated     bool           `json:"truncated"`
	Findings      []Finding      `json:"findings"`
}

type Facts struct {
	CommittedDomains Fact[[]string]  `json:"committed_domains"`
	OS               Fact[target.OS] `json:"os"`
	Arch             Fact[string]    `json:"arch"`
	Systemd          Fact[string]    `json:"systemd"`
	Podman           Fact[string]    `json:"podman"`
	Passt            Fact[string]    `json:"passt"`
	Caddy            Fact[string]    `json:"caddy"`
	CgroupV2         Fact[bool]      `json:"cgroup_v2"`
	UIDMap           Fact[bool]      `json:"uidmap"`
	DBus             Fact[bool]      `json:"dbus_user_session"`
	Polkit           Fact[bool]      `json:"polkitd"`
	Linger           Fact[bool]      `json:"linger"`
	UserBus          Fact[bool]      `json:"user_bus"`
	CaddyActive      Fact[bool]      `json:"caddy_active"`
	AdminLoopback    Fact[bool]      `json:"admin_loopback"`
	ProxyConflict    Fact[bool]      `json:"proxy_conflict"`
	CatchAll         Fact[bool]      `json:"catch_all"`
	FreeDiskBytes    Fact[uint64]    `json:"free_disk_bytes"`
	Policy           Fact[Policy]    `json:"policy"`
	Control          Fact[Control]   `json:"control_store"`
}
type Policy struct {
	State                string `json:"state"`
	MinimumFreeDiskBytes uint64 `json:"minimum_free_disk_bytes"`
}
type Control struct {
	State      string     `json:"state"`
	QuickCheck Fact[bool] `json:"quick_check"`
}
type Registry struct {
	Host      string     `json:"host"`
	Reachable Fact[bool] `json:"reachable"`
}
type Domain struct {
	Name string       `json:"name"`
	DNS  Fact[string] `json:"dns"`
	TLS  Fact[string] `json:"tls"`
}

// Backup summarizes the existing status contract, never credentials or paths.
// Unknown loss and SQLite local position are not host health measurements.
type Backup struct {
	App      string       `json:"app"`
	Database string       `json:"database"`
	Engine   string       `json:"engine"`
	State    Fact[string] `json:"state"`
	Stale    Fact[bool]   `json:"stale"`
}

func emptyReport() Report {
	return Report{SchemaVersion: 1, Ready: UnknownReady, Facts: Facts{
		CommittedDomains: Unknown[[]string]("store_unavailable"), OS: Unknown[target.OS]("inventory_unavailable"), Arch: Unknown[string]("inventory_unavailable"),
		Systemd: Unknown[string]("inventory_unavailable"), Podman: Unknown[string]("inventory_unavailable"), Passt: Unknown[string]("inventory_unavailable"), Caddy: Unknown[string]("inventory_unavailable"),
		CgroupV2: Unknown[bool]("inventory_unavailable"), UIDMap: Unknown[bool]("probe_unavailable"), DBus: Unknown[bool]("probe_unavailable"), Polkit: Unknown[bool]("probe_unavailable"), Linger: Unknown[bool]("inventory_unavailable"), UserBus: Unknown[bool]("probe_unavailable"), CaddyActive: Unknown[bool]("probe_unavailable"), AdminLoopback: Unknown[bool]("probe_unavailable"), ProxyConflict: Unknown[bool]("inventory_unavailable"), CatchAll: Unknown[bool]("probe_unavailable"), FreeDiskBytes: Unknown[uint64]("inventory_unavailable"), Policy: Unknown[Policy]("policy_unavailable"), Control: Unknown[Control]("store_unavailable")},
		Registries: []Registry{}, Domains: []Domain{}, Backups: Unknown[[]Backup]("store_unavailable"), Findings: []Finding{}}
}
