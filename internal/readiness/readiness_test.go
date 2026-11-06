package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

func readyReport() Report {
	r := emptyReport()
	r.Facts = Facts{CommittedDomains: Known([]string{}), OS: Known(target.OS{ID: "debian", Version: "13"}), Arch: Known("arm64"), Systemd: Known("257.8"), Podman: Known("5.4.2"), Passt: Known("2025.01"), Caddy: Known("2.6.2"), CgroupV2: Known(true), UIDMap: Known(true), DBus: Known(true), Polkit: Known(true), Linger: Known(true), UserBus: Known(true), CaddyActive: Known(true), AdminLoopback: Known(true), ProxyConflict: Known(false), CatchAll: Known(false), FreeDiskBytes: Known(uint64(2 << 30)), Policy: Known(Policy{State: "valid", MinimumFreeDiskBytes: 1 << 30}), Control: Known(Control{State: "current", QuickCheck: Known(true)})}
	r.Backups = Known([]Backup{})
	Evaluate(&r)
	return r
}

type ruleCase struct {
	code        string
	breakFact   func(*Report)
	unknownFact func(*Report)
}

func ruleCases() []ruleCase {
	return []ruleCase{
		{"os_unsupported", func(r *Report) { r.Facts.OS = Known(target.OS{ID: "other", Version: "13"}) }, func(r *Report) { r.Facts.OS = Unknown[target.OS]("inventory_unknown") }},
		{"runtime_version_mismatch", func(r *Report) { r.Facts.Podman = Known("5.5.1") }, func(r *Report) { r.Facts.Podman = Unknown[string]("inventory_unknown") }},
		{"cgroup_unsupported", func(r *Report) { r.Facts.CgroupV2 = Known(false) }, func(r *Report) { r.Facts.CgroupV2 = Unknown[bool]("inventory_unknown") }},
		{"rootless_helper_missing", func(r *Report) { r.Facts.UIDMap = Known(false) }, func(r *Report) { r.Facts.UIDMap = Unknown[bool]("inventory_unknown") }},
		{"user_session_missing", func(r *Report) { r.Facts.UserBus = Known(false) }, func(r *Report) { r.Facts.UserBus = Unknown[bool]("inventory_unknown") }},
		{"disk_below_reserve", func(r *Report) { r.Facts.FreeDiskBytes = Known(uint64(0)) }, func(r *Report) { r.Facts.FreeDiskBytes = Unknown[uint64]("inventory_unknown") }},
		{"policy_invalid", func(r *Report) { r.Facts.Policy = Known(Policy{State: "invalid"}) }, func(r *Report) { r.Facts.Policy = Unknown[Policy]("policy_unavailable") }},
		{"policy_denies_deploy", func(r *Report) { r.Facts.Policy = Known(Policy{State: "denies_deploy"}) }, func(r *Report) { r.Facts.Policy = Unknown[Policy]("policy_unavailable") }},
		{"caddy_inactive", func(r *Report) { r.Facts.CaddyActive = Known(false) }, func(r *Report) { r.Facts.CaddyActive = Unknown[bool]("inventory_unknown") }},
		{"caddy_admin_exposed", func(r *Report) { r.Facts.AdminLoopback = Known(false) }, func(r *Report) { r.Facts.AdminLoopback = Unknown[bool]("inventory_unknown") }},
		{"proxy_conflict", func(r *Report) { r.Facts.ProxyConflict = Known(true) }, func(r *Report) { r.Facts.ProxyConflict = Unknown[bool]("inventory_unknown") }},
		{"catch_all_site", func(r *Report) { r.Facts.CatchAll = Known(true) }, func(r *Report) { r.Facts.CatchAll = Unknown[bool]("inventory_unknown") }},
		{"control_store_missing", func(r *Report) {
			r.Facts.Control = Known(Control{State: "missing", QuickCheck: Unknown[bool]("store_unavailable")})
		}, func(r *Report) { r.Facts.Control = Unknown[Control]("store_unavailable") }},
		{"control_store_unhealthy", func(r *Report) { r.Facts.Control = Known(Control{State: "current", QuickCheck: Known(false)}) }, func(r *Report) {
			r.Facts.Control = Known(Control{State: "current", QuickCheck: Unknown[bool]("probe_timeout")})
		}},
		{"control_store_upgrade_required", func(r *Report) {
			r.Facts.Control = Known(Control{State: "schema_upgrade_required", QuickCheck: Unknown[bool]("store_unavailable")})
		}, func(r *Report) { r.Facts.Control = Unknown[Control]("store_unavailable") }},
		{"registry_unreachable", func(r *Report) { r.Registries = []Registry{{Host: "registry.example.com", Reachable: Known(false)}} }, func(r *Report) {
			r.Registries = []Registry{{Host: "registry.example.com", Reachable: Unknown[bool]("probe_timeout")}}
		}},
		{"domain_unresolved", func(r *Report) {
			r.Domains = []Domain{{Name: "app.example.com", DNS: Known("unresolved"), TLS: Known("trusted")}}
		}, func(r *Report) {
			r.Domains = []Domain{{Name: "app.example.com", DNS: Unknown[string]("probe_failed"), TLS: Known("trusted")}}
		}},
		{"domain_elsewhere", func(r *Report) {
			r.Domains = []Domain{{Name: "app.example.com", DNS: Known("resolves_elsewhere"), TLS: Known("trusted")}}
		}, func(r *Report) {
			r.Domains = []Domain{{Name: "app.example.com", DNS: Known("unknown_behind_nat"), TLS: Known("trusted")}}
		}},
		{"tls_untrusted", func(r *Report) {
			r.Domains = []Domain{{Name: "app.example.com", DNS: Known("resolves_here"), TLS: Known("untrusted")}}
		}, func(r *Report) {
			r.Domains = []Domain{{Name: "app.example.com", DNS: Known("resolves_here"), TLS: Unknown[string]("probe_timeout")}}
		}},
		{"backup_stale", func(r *Report) {
			r.Backups = Known([]Backup{{App: "app", Database: "db", Engine: "sqlite", State: Known("degraded"), Stale: Known(true)}})
		}, func(r *Report) {
			r.Backups = Known([]Backup{{App: "app", Database: "db", Engine: "sqlite", State: Known("degraded"), Stale: Unknown[bool]("observation_unknown")}})
		}},
	}
}
func TestReadinessRules(t *testing.T) {
	golden := map[string]json.RawMessage{}
	for _, test := range ruleCases() {
		t.Run(test.code, func(t *testing.T) { golden[test.code] = testReadinessRule(t, test) })
	}
	raw, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	assertReadinessGolden(t, "rules.json", string(raw)+"\n")
}
func testReadinessRule(t *testing.T, test ruleCase) json.RawMessage {
	t.Helper()
	r := readyReport()
	test.breakFact(&r)
	Evaluate(&r)
	if r.Ready != NotReady || len(r.Findings) != 1 || r.Findings[0].Code != test.code {
		t.Fatalf("unexpected failure: %+v", r)
	}
	raw, _ := json.Marshal(r)
	if _, err := DecodeReport(raw); err != nil {
		t.Fatal(err)
	}
	golden, _ := json.Marshal(r.Findings)
	test.unknownFact(&r)
	Evaluate(&r)
	if r.Ready != UnknownReady || len(r.Findings) != 0 {
		t.Fatalf("unknown became health or failure: %+v", r)
	}
	return golden
}
func assertReadinessGolden(t *testing.T, name, value string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(path) // #nosec G304 -- Fixed local testdata golden.
	if err != nil || string(expected) != value {
		t.Fatalf("golden %s differs: %v", path, err)
	}
}
func TestReadinessDecoderRejectsForgedHealthAndUnknowns(t *testing.T) {
	r := readyReport()
	raw, _ := json.Marshal(r)
	if _, err := DecodeReport(raw); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{strings.Replace(string(raw), `"ready":true`, `"ready":false`, 1), strings.Replace(string(raw), `"facts":{`, `"facts":{"arch":{"status":"known","value":"arm64"},`, 1), strings.Replace(string(raw), `"reason":"inventory_unknown"`, `"reason":"planted-secret"`, 1), `null`, strings.Repeat(" ", MaxReportBytes+1)} {
		if candidate == string(raw) {
			continue
		}
		if _, err := DecodeReport([]byte(candidate)); err == nil {
			t.Fatal("forged report accepted")
		}
	}
	unknown := emptyReport()
	Evaluate(&unknown)
	bytes, _ := json.Marshal(unknown)
	if _, err := DecodeReport(bytes); err != nil {
		t.Fatal(err)
	}
}

type inventoryFunc func(context.Context) (target.Snapshot, error)

func (f inventoryFunc) Collect(ctx context.Context) (target.Snapshot, error) { return f(ctx) }

type runtimeFunc func(context.Context) (inventory.ReadinessFacts, error)

func (f runtimeFunc) Readiness(ctx context.Context) (inventory.ReadinessFacts, error) { return f(ctx) }

type policyFixture struct{ p policy.Policy }

func (p policyFixture) Load(context.Context) (policy.Policy, error) { return p.p, nil }

func TestReadinessHungProbePartialAndSlots(t *testing.T) {
	release := make(chan struct{})
	exited := make(chan struct{}, 4)
	entered := make(chan struct{}, 4)
	finished := make(chan struct{}, 4)
	var active, peak atomic.Int32
	fn := func(context.Context) (bool, error) {
		entered <- struct{}{}
		n := active.Add(1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		<-release
		active.Add(-1)
		exited <- struct{}{}
		return true, nil
	}
	defer func() {
		close(release)
		for range 4 {
			<-exited
			<-finished
		}
	}()
	for range 4 {
		go func() { _, _ = bounded(t.Context(), time.Second, fn); finished <- struct{}{} }()
		<-entered
	}
	for range 4 {
		_, _ = bounded(t.Context(), time.Millisecond, fn)
	}
	if active.Load() != 4 || peak.Load() != 4 {
		t.Fatal("outstanding probe limit failed", active.Load(), peak.Load())
	}
	// Saturation still returns a bounded partial report, without a fifth probe.
	start := time.Now()
	r, err := (Reader{Inventory: inventoryFunc(func(context.Context) (target.Snapshot, error) {
		t.Error("fifth probe launched")
		return target.Snapshot{}, nil
	}), Timeout: 5 * time.Millisecond}).Read(t.Context())
	if err != nil || r.Ready != UnknownReady || len(r.Findings) != 0 || time.Since(start) > time.Second {
		t.Fatal(r, err)
	}
}
func TestReadinessPartialCollectionRedactsFailures(t *testing.T) {
	r, err := (Reader{Inventory: inventoryFunc(func(context.Context) (target.Snapshot, error) {
		return target.Snapshot{OS: target.OS{ID: "debian", Version: "13"}, Arch: "arm64"}, errors.New("planted-secret")
	}), Runtime: runtimeFunc(func(context.Context) (inventory.ReadinessFacts, error) {
		return inventory.ReadinessFacts{}, errors.New("planted-secret")
	})}).Read(t.Context())
	raw, _ := json.Marshal(r)
	if err != nil || r.Ready != UnknownReady || strings.Contains(string(raw), "planted-secret") || r.Facts.OS.Value == nil {
		t.Fatal(r, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (Reader{}).Read(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestReadinessEveryBooleanUnknownAndKnownControl(t *testing.T) {
	initial := readyReport()
	f := reflect.ValueOf(&initial.Facts).Elem()
	for i := 0; i < f.NumField(); i++ {
		testReadinessBoolean(t, i)
	}
}
func testReadinessBoolean(t *testing.T, index int) {
	t.Helper()
	r := readyReport()
	field := reflect.ValueOf(&r.Facts).Elem().Field(index)
	if field.Type() != reflect.TypeFor[Fact[bool]]() {
		return
	}
	field.Set(reflect.ValueOf(Unknown[bool]("probe_failed")))
	Evaluate(&r)
	if r.Ready != UnknownReady || len(r.Findings) != 0 {
		t.Fatalf("unknown boolean %d: %+v", index, r)
	}
}

type scopeNetwork struct{ calls []string }

func (n *scopeNetwork) Registry(_ context.Context, name string) (bool, error) {
	n.calls = append(n.calls, "registry:"+name)
	return true, nil
}
func (n *scopeNetwork) DNS(_ context.Context, name string, _ target.Observation[[]string]) (string, error) {
	n.calls = append(n.calls, "dns:"+name)
	return "resolves_here", nil
}
func (n *scopeNetwork) TLS(_ context.Context, name string) (string, error) {
	n.calls = append(n.calls, "tls:"+name)
	return "trusted", nil
}
func TestReadinessPolicyDerivesProbeScope(t *testing.T) {
	p, err := policy.Parse([]byte(`schema_version=1
version="fixture"
allowed_domains=["app.example.com"]
[resources]
memory_mb=512
pids_limit=128
[[allowed_registries]]
host="registry.example.com:5443"
repository_prefixes=["approved/repo","approved/other"]
`))
	if err != nil {
		t.Fatal(err)
	}
	network := &scopeNetwork{}
	inventory := inventoryFunc(func(context.Context) (target.Snapshot, error) {
		return target.Snapshot{OS: target.OS{ID: "debian", Version: "13"}, Arch: "arm64", CaddyConfig: target.Known(target.CaddyConfigSet{Files: []target.CaddyFile{}}), LiveCaddyFiles: target.Known([]target.LiveCaddyFile{{Name: "app.caddy", App: "app", Domains: target.Known([]string{"app.example.com"})}})}, nil
	})
	r, err := (Reader{Inventory: inventory, Policy: policyFixture{p}, Network: network, Store: scopeStore{}}).Read(t.Context())
	if err != nil || r.Facts.Policy.Value == nil || r.Facts.Policy.Value.State != "valid" || !reflect.DeepEqual(network.calls, []string{"registry:registry.example.com:5443", "dns:app.example.com", "tls:app.example.com"}) {
		t.Fatal(r, err, network.calls)
	}
	raw, _ := json.Marshal(r)
	if _, err := DecodeReport(raw); err != nil {
		t.Fatal(err)
	}
}

type scopeStore struct{}

func (scopeStore) Control(context.Context) (Control, error) {
	return Control{State: "current", QuickCheck: Known(true)}, nil
}
func (scopeStore) Backups(context.Context) ([]Backup, error) { return []Backup{}, nil }
func (scopeStore) Routes(context.Context) ([]string, error)  { return []string{"app.example.com"}, nil }

type invalidPolicy struct{ err error }

func (f invalidPolicy) Load(context.Context) (policy.Policy, error) { return policy.Policy{}, f.err }
func TestReadinessPolicyUnknownAndInvalidControls(t *testing.T) {
	_, invalid := policy.Parse([]byte("not valid policy"))
	for _, test := range []struct {
		err  error
		want string
	}{{invalid, "invalid"}, {errors.New("planted-private-policy"), "unknown"}} {
		r, err := (Reader{Policy: invalidPolicy{test.err}}).Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if test.want == "unknown" && r.Facts.Policy.Value != nil {
			t.Fatal("unreadable policy falsely invalid", r)
		}
		if test.want == "invalid" && (r.Facts.Policy.Value == nil || r.Facts.Policy.Value.State != "invalid") {
			t.Fatal("invalid policy lost", r)
		}
	}
	p, err := policy.Parse([]byte("schema_version=1\nversion=\"fixture\"\n[resources]\nmemory_mb=512\npids_limit=128\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := (Reader{Policy: policyFixture{p}}).Read(t.Context())
	if err != nil || r.Facts.Policy.Value == nil || r.Facts.Policy.Value.State != "denies_deploy" || r.Ready != NotReady {
		t.Fatal(r, err)
	}
}
func TestReadinessUncommittedLiveRouteDoesNotTriggerTLS(t *testing.T) {
	n := &scopeNetwork{}
	r := Reader{Network: n}
	snapshot := target.Snapshot{LocalAddresses: target.Known([]string{"192.0.2.10"}), LiveCaddyFiles: target.Known([]target.LiveCaddyFile{{App: "app", Name: "app.caddy", Domains: target.Known([]string{"app.example.com"})}})}
	domain := r.domain(t.Context(), "app.example.com", snapshot, Known([]string{}))
	if domain.TLS.Status != "absent" || !reflect.DeepEqual(n.calls, []string{"dns:app.example.com"}) {
		t.Fatal(domain, n.calls)
	}
}
func TestReadinessVersionAndOwnershipControls(t *testing.T) {
	for _, test := range []struct {
		version string
		want    bool
	}{{"257", true}, {"257.8", true}, {"2570", false}, {"256", false}} {
		value := prefix(Known(test.version), "257")
		if value.Value == nil || *value.Value != test.want {
			t.Fatal(test, value)
		}
	}
	value := proxyConflict(target.Known([]target.PortOwner{{Port: 443, Process: "caddy"}}))
	if value.Value != nil {
		t.Fatal("process name alone became Caddy ownership", value)
	}
	value = proxyConflict(target.Known([]target.PortOwner{{Port: 443, Process: "caddy", Unit: "caddy.service"}}))
	if value.Value == nil || *value.Value {
		t.Fatal(value)
	}
}
