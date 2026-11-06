package readiness

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

type Inventory interface {
	Collect(context.Context) (target.Snapshot, error)
}
type Runtime interface {
	Readiness(context.Context) (inventory.ReadinessFacts, error)
}
type PolicyLoader interface {
	Load(context.Context) (policy.Policy, error)
}
type ControlReader interface {
	Control(context.Context) (Control, error)
	Backups(context.Context) ([]Backup, error)
	Routes(context.Context) ([]string, error)
}

// Network accepts only destinations selected by the protected policy composer.
type Network interface {
	Registry(context.Context, string) (bool, error)
	DNS(context.Context, string, target.Observation[[]string]) (string, error)
	TLS(context.Context, string) (string, error)
}
type Reader struct {
	Inventory Inventory
	Runtime   Runtime
	Policy    PolicyLoader
	Store     ControlReader
	Network   Network
	Timeout   time.Duration
}

// Slots remain held until even a cancellation-ignoring dependency exits.
var probeSlots = make(chan struct{}, 4)

func bounded[T any](parent context.Context, timeout time.Duration, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	select {
	case probeSlots <- struct{}{}:
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-probeSlots
		return zero, err
	}
	type reply struct {
		value T
		err   error
	}
	done := make(chan reply, 1)
	go func() { defer func() { <-probeSlots }(); value, err := fn(ctx); done <- reply{value, err} }()
	select {
	case out := <-done:
		return out.value, out.err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
func fact[T any](ctx context.Context, timeout time.Duration, fn func(context.Context) (T, error)) Fact[T] {
	value, err := bounded(ctx, timeout, fn)
	if err != nil {
		return Unknown[T](probeReason(err))
	}
	return Known(value)
}
func probeReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "probe_timeout"
	}
	return "probe_failed"
}

func (r Reader) Read(parent context.Context) (Report, error) {
	timeout := r.Timeout
	if timeout <= 0 || timeout > TotalTimeout {
		timeout = TotalTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	out := emptyReport()
	snapshot := target.Snapshot{}
	if r.Inventory != nil {
		s, err := bounded(ctx, InventoryTimeout, r.Inventory.Collect)
		// Collect returns already-measured partial facts when a later probe fails.
		snapshot = s
		attachInventory(&out.Facts, s)
		if err != nil && s.OS.ID == "" {
			out.Facts.OS = Unknown[target.OS](probeReason(err))
		}
	}
	if r.Runtime != nil {
		extra, err := bounded(ctx, InventoryTimeout, r.Runtime.Readiness)
		if err == nil {
			attachRuntime(&out.Facts, extra)
		}
	}
	if r.Store != nil {
		out.Facts.Control = fact(ctx, ProbeTimeout, r.Store.Control)
		out.Facts.CommittedDomains = fact(ctx, ProbeTimeout, r.Store.Routes)
		out.Backups = fact(ctx, ProbeTimeout, r.Store.Backups)
	}
	if r.Policy != nil {
		r.policy(ctx, &out, snapshot)
	}
	if err := parent.Err(); err != nil {
		return Report{}, err
	}
	Evaluate(&out)
	return out, nil
}
func attachInventory(f *Facts, s target.Snapshot) {
	if tokenPattern.MatchString(s.OS.ID) && tokenPattern.MatchString(s.OS.Version) {
		f.OS = Known(s.OS)
	}
	if tokenPattern.MatchString(s.Arch) {
		f.Arch = Known(s.Arch)
	}
	f.Systemd, f.Podman, f.Passt, f.Caddy = versionFact(s.Versions.Systemd), versionFact(s.Versions.Podman), versionFact(s.Versions.Passt), versionFact(s.Versions.Caddy)
	f.CgroupV2, f.Linger, f.FreeDiskBytes = observed(s.CgroupV2), observed(s.Runner.Linger), observed(s.FreeDiskBytes)
	f.ProxyConflict = proxyConflict(s.PortOwners)
}
func attachRuntime(f *Facts, r inventory.ReadinessFacts) {
	f.UIDMap, f.DBus, f.Polkit, f.UserBus = observed(r.UIDMap), observed(r.DBus), observed(r.Polkit), observed(r.UserBus)
	f.CaddyActive, f.AdminLoopback, f.CatchAll = observed(r.CaddyActive), observed(r.AdminLoopback), observed(r.CatchAll)
}
func proxyConflict(o target.Observation[[]target.PortOwner]) Fact[bool] {
	if o.Value == nil {
		return Unknown[bool]("inventory_unknown")
	}
	uncertain := false
	for _, owner := range *o.Value {
		if owner.Port != 80 && owner.Port != 443 {
			continue
		}
		if owner.Process == "" && owner.Unit == "" {
			uncertain = true
			continue
		}
		if owner.Process == "caddy" && owner.Unit == "" {
			uncertain = true
			continue
		}
		if owner.Process != "caddy" || owner.Unit != "caddy.service" {
			return Known(true)
		}
	}
	if uncertain {
		return Unknown[bool]("inventory_unknown")
	}
	return Known(false)
}
func (r Reader) policy(ctx context.Context, out *Report, s target.Snapshot) {
	p, err := bounded(ctx, ProbeTimeout, r.Policy.Load)
	if err != nil {
		var invalid *policy.Refusal
		if errors.As(err, &invalid) {
			out.Facts.Policy = Known(Policy{State: "invalid"})
		}
		return
	}
	if p.Hash() == "" {
		return
	}
	permissions := p.Permissions()
	hosts := []string{}
	for _, registry := range permissions.Registries {
		if len(registry.RepositoryPrefixes) > 0 {
			hosts = append(hosts, registry.Host)
		}
	}
	state := "valid"
	if len(hosts) == 0 || len(permissions.Domains) == 0 {
		state = "denies_deploy"
	}
	out.Facts.Policy = Known(Policy{State: state, MinimumFreeDiskBytes: p.MinimumFreeDiskBytes()})
	slices.Sort(hosts)
	hosts = slices.Compact(hosts)
	domains := []string{}
	for _, domain := range permissions.Domains {
		if !validExactDomain(domain) {
			out.Truncated = true
			continue
		}
		domains = append(domains, domain)
	}
	slices.Sort(domains)
	out.Truncated = out.Truncated || len(hosts) > MaxItems || len(domains) > MaxItems
	for _, host := range hosts[:min(len(hosts), MaxItems)] {
		out.Registries = append(out.Registries, r.registry(ctx, host))
	}
	for _, domain := range domains[:min(len(domains), MaxItems)] {
		out.Domains = append(out.Domains, r.domain(ctx, domain, s, out.Facts.CommittedDomains))
	}
}
func (r Reader) registry(ctx context.Context, host string) Registry {
	value := Registry{Host: host, Reachable: Unknown[bool]("probe_unavailable")}
	if r.Network != nil {
		value.Reachable = fact(ctx, ProbeTimeout, func(ctx context.Context) (bool, error) { return r.Network.Registry(ctx, host) })
	}
	return value
}
func (r Reader) domain(ctx context.Context, name string, s target.Snapshot, committed Fact[[]string]) Domain {
	value := Domain{Name: name, DNS: Unknown[string]("probe_unavailable"), TLS: Unknown[string]("route_unknown")}
	if r.Network != nil {
		value.DNS = fact(ctx, ProbeTimeout, func(ctx context.Context) (string, error) { return r.Network.DNS(ctx, name, s.LocalAddresses) })
	}
	route := check(committed, func(domains []string) bool { return slices.Contains(domains, name) })
	if route.Value != nil && !*route.Value {
		value.TLS = Fact[string]{Status: "absent", Reason: "no_committed_route"}
	}
	if route.Value != nil && *route.Value && r.Network != nil {
		value.TLS = fact(ctx, ProbeTimeout, func(ctx context.Context) (string, error) { return r.Network.TLS(ctx, name) })
	}
	return value
}

func versionFact(o target.Observation[string]) Fact[string] {
	f := observed(o)
	if !validValue(f, tokenPattern.MatchString) {
		return Unknown[string]("inventory_unknown")
	}
	return f
}
