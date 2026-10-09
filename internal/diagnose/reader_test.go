package diagnose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type fakeInventory struct {
	snapshot target.Snapshot
	err      error
}

func (f fakeInventory) Collect(context.Context) (target.Snapshot, error) { return f.snapshot, f.err }

type fakeStore struct {
	names   []string
	records []ops.OperationRecord
	release ops.Release
	err     error
}

func (f fakeStore) AppNames(context.Context) ([]string, error) { return f.names, f.err }
func (f fakeStore) RecentOperations(context.Context, string, int) ([]ops.OperationRecord, error) {
	return f.records, f.err
}
func (f fakeStore) CurrentRelease(context.Context, string) (ops.Release, error) {
	if f.err != nil {
		return ops.Release{}, f.err
	}
	return f.release, nil
}
func (f fakeStore) PreviousRelease(context.Context, string) (ops.Release, error) {
	return ops.Release{}, store.ErrNotFound
}
func (f fakeStore) LoadPlan(context.Context, string) (plan.Plan, policy.Desired, error) {
	return plan.Plan{}, policy.Desired{}, errors.New("password=store-secret")
}

type fakeRuntime struct{}

func (fakeRuntime) RunStdout(_ context.Context, path string, args ...string) (string, error) {
	switch path {
	case "systemctl":
		return "ActiveState=failed\nSubState=failed\nNRestarts=3\n", nil
	case "podman":
		return `{"name":"systemd-demo","running":false,"unit":"demo.service"}`, nil
	}
	return "", errors.New("password=runtime-secret")
}

type fakeJournal struct{}

func (fakeJournal) Execute(context.Context, localexec.Command) (localexec.Result, error) {
	return localexec.Result{Stdout: `{"__REALTIME_TIMESTAMP":"1791504000000000","PRIORITY":"3","MESSAGE":"password=journal-secret Authorization: Bearer token-secret"}` + "\n"}, nil
}
func fixtureSnapshot() target.Snapshot {
	hash := "sha256:" + strings.Repeat("a", 64)
	return target.Snapshot{OS: target.OS{ID: "debian", Version: "13"}, Arch: "arm64", Versions: target.Versions{Systemd: target.Known("257"), Podman: target.Known("5.4"), Caddy: target.Known("2.6"), Passt: target.Observation[string]{Status: target.Unknown}, Litestream: target.Observation[string]{Status: target.Absent}}, FreeDiskBytes: target.Known(uint64(1)), Runner: target.Runner{User: target.Known("brine"), Linger: target.Known(false)}, CaddyConfig: target.Known(target.CaddyConfigSet{Generation: 2, Files: []target.CaddyFile{{Name: "demo.caddy", Hash: hash}}}), LiveCaddyFiles: target.Known([]target.LiveCaddyFile{}), Apps: target.Known([]target.App{{Name: "demo", Image: target.Known(target.Image{Digest: hash, Platform: target.Platform{OS: "linux", Arch: "arm64"}}), AllocatedHostPort: target.Known(target.Port(20000)), QuadletUnits: target.Known([]target.Unit{{Name: "demo.container", Hash: hash}}), Secrets: target.Known([]target.Secret{})}})}
}
func fixtureReader() Reader {
	snapshot := fixtureSnapshot()
	hash := "sha256:" + strings.Repeat("a", 64)
	return Reader{Inventory: fakeInventory{snapshot: snapshot}, Runner: fakeRuntime{}, Logs: logs.Reader{Inventory: fakeInventory{snapshot: snapshot}, Executor: fakeJournal{}}, Store: fakeStore{names: []string{"demo"}, records: []ops.OperationRecord{{Operation: ops.Operation{ID: "op-fixture", State: ops.Failed, UpdatedAt: time.Unix(0, 0).UTC()}, FailureCode: "stale_plan"}}, release: ops.Release{ID: "release-2", PlanID: hash, CaddyGeneration: 2, HostPort: 20000, Image: plan.Image{Digest: hash, Platform: target.Platform{OS: "linux", Arch: "arm64"}}, Units: []target.Unit{{Name: "demo.container", Hash: "sha256:" + strings.Repeat("b", 64)}}, Secrets: []plan.SecretBinding{}, CaddyFile: target.CaddyFile{Name: "demo.caddy", Hash: hash}}}, MinimumFreeDiskBytes: func(context.Context) (uint64, error) { return 2, nil }}
}
func TestReaderEndToEndAndRedaction(t *testing.T) {
	report, err := fixtureReader().Read(context.Background(), Request{App: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Apps) != 1 {
		t.Fatal(report)
	}
	a := report.Apps[0]
	if a.Unit.Value == nil || a.Unit.Value.Restarts != 3 || a.ContainerRunning.Value == nil || *a.ContainerRunning.Value {
		t.Fatalf("runtime facts %+v", a)
	}
	if a.PreviousRelease.Status != "absent" || a.CurrentRelease.Value.ID != "release-2" || a.Operations.Value == nil || (*a.Operations.Value)[0].FailureCode != "stale_plan" {
		t.Fatal(a)
	}
	if a.Drift.Value == nil || !reflect.DeepEqual(*a.Drift.Value, []string{"units"}) {
		t.Fatal(a.Drift)
	}
	if a.Logs.Value == nil || len(*a.Logs.Value) != 1 {
		t.Fatal(a.Logs)
	}
	if report.Host.LiveGeneration.Status != "unknown" || report.Host.SelectedGeneration.Value == nil || *report.Host.SelectedGeneration.Value != 2 {
		t.Fatal(report.Host)
	}
	raw, _ := json.Marshal(report)
	for _, secret := range []string{"journal-secret", "token-secret", "store-secret", "runtime-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	decoded, err := DecodeReport(raw)
	if err != nil || !reflect.DeepEqual(report, decoded) {
		t.Fatalf("round trip %v", err)
	}
}
func TestOwnBudgetReturnsUnknown(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	start := time.Now()
	report, err := (Reader{Inventory: hangingInventory{release}, Timeout: 25 * time.Millisecond}).Read(context.Background(), Request{App: "demo"})
	if err != nil || time.Since(start) > time.Second || report.Host.OS.Reason != "probe_timeout" || len(report.Apps) != 1 {
		t.Fatalf("%+v %v", report, err)
	}
}
func TestProbeErrorTextNeverEntersReport(t *testing.T) {
	reader := Reader{Inventory: fakeInventory{err: errors.New("password=private-error")}, Store: fakeStore{err: errors.New("password=private-error")}}
	report, e := reader.Read(context.Background(), Request{App: "demo"})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "private-error") {
		t.Fatal(string(raw))
	}
}
func TestAppLimitAndStableOrder(t *testing.T) {
	r := Reader{Store: fakeStore{err: store.ErrNotFound}, Inventory: fakeInventory{snapshot: fixtureSnapshot()}}
	s := fixtureSnapshot()
	apps := []target.App{}
	for i := 20; i >= 0; i-- {
		apps = append(apps, target.App{Name: fmt.Sprintf("app-%02d", i)})
	}
	s.Apps = target.Known(apps)
	r.Inventory = fakeInventory{snapshot: s}
	report, e := r.Read(context.Background(), Request{})
	if e != nil {
		t.Fatal(e)
	}
	if !report.Truncated || len(report.Apps) != MaxApps || report.Apps[0].Name != "app-00" || report.Apps[15].Name != "app-15" {
		t.Fatal(report)
	}
}
func TestDecodeReportRedactsAgain(t *testing.T) {
	report, e := fixtureReader().Read(context.Background(), Request{App: "demo"})
	if e != nil {
		t.Fatal(e)
	}
	report.Apps[0].Logs = Known([]logs.Line{{Timestamp: "2026-10-09T00:00:00Z", Priority: 6, Message: "password=boundary-secret"}})
	raw, _ := json.Marshal(report)
	decoded, e := DecodeReport(raw)
	if e != nil {
		t.Fatal(e)
	}
	clean, _ := json.Marshal(decoded)
	if strings.Contains(string(clean), "boundary-secret") {
		t.Fatal(string(clean))
	}
}
func TestDecodeRejectsInvalidReport(t *testing.T) {
	report, e := fixtureReader().Read(context.Background(), Request{App: "demo"})
	if e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*Report){func(r *Report) { r.SchemaVersion = 2 }, func(r *Report) { r.Host.Arch = Fact[string]{Status: "known"} }, func(r *Report) { r.Apps[0].Name = "bad;id" }, func(r *Report) { r.Findings[0].Message = "made up" }, func(r *Report) { r.Apps = append(r.Apps, r.Apps[0]) }} {
		raw, _ := json.Marshal(report)
		var copy Report
		_ = json.Unmarshal(raw, &copy)
		mutate(&copy)
		raw, _ = json.Marshal(copy)
		if _, e := DecodeReport(raw); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestDriftUnknownAndEveryArtifact(t *testing.T) {
	reader := fixtureReader()
	release := reader.Store.(fakeStore).release
	s := fixtureSnapshot()
	a := &(*s.Apps.Value)[0]
	release.Units = *a.QuadletUnits.Value
	s.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{{App: "demo", Domains: target.Known([]string{})}})
	if got := drift(s, "demo", release, Known(policy.Desired{})); got.Value == nil || len(*got.Value) != 0 {
		t.Fatal(got)
	}
	tests := []struct {
		code   string
		change func(*target.Snapshot, *target.App)
	}{
		{"image", func(_ *target.Snapshot, a *target.App) { a.Image.Value.Digest = "sha256:" + strings.Repeat("c", 64) }},
		{"host_port", func(_ *target.Snapshot, a *target.App) { a.AllocatedHostPort = target.Known(target.Port(20001)) }},
		{"secret_bindings", func(_ *target.Snapshot, a *target.App) {
			a.Secrets = target.Known([]target.Secret{{Name: "brine.demo.key.v2", ID: "different"}})
		}},
		{"caddy_file", func(s *target.Snapshot, _ *target.App) {
			s.CaddyConfig.Value.Files[0].Hash = "sha256:" + strings.Repeat("c", 64)
		}},
		{"caddy_file_missing", func(s *target.Snapshot, _ *target.App) { s.CaddyConfig.Value.Files = []target.CaddyFile{} }},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			s := fixtureSnapshot()
			s.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{{App: "demo", Domains: target.Known([]string{})}})
			expected := release
			if tt.code == "secret_bindings" {
				expected.Secrets = []plan.SecretBinding{{VersionName: "brine.demo.key.v2", ID: "expected"}}
			}
			a := &(*s.Apps.Value)[0]
			tt.change(&s, a)
			got := drift(s, "demo", expected, Known(policy.Desired{}))
			if got.Value == nil || !reflect.DeepEqual(*got.Value, []string{tt.code}) {
				t.Fatal(got)
			}
		})
	}
	a.Image = target.Observation[target.Image]{Status: target.Unknown}
	if got := drift(s, "demo", release, Known(policy.Desired{})); got.Status != "unknown" {
		t.Fatal(got)
	}
}

type healthStore struct {
	fakeStore
	desired policy.Desired
}

func (h healthStore) LoadPlan(context.Context, string) (plan.Plan, policy.Desired, error) {
	return plan.Plan{}, h.desired, nil
}
func TestHealthIsBoundedLoopbackAndDoesNotRedirect(t *testing.T) {
	for _, status := range []int{200, 503, 302} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/health" {
					t.Errorf("followed health redirect to %s", r.URL.Path)
				}
				w.Header().Set("Location", "/other")
				w.WriteHeader(status)
			}))
			defer server.Close()
			_, port, e := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
			if e != nil {
				t.Fatal(e)
			}
			number, e := strconv.ParseUint(port, 10, 16)
			if e != nil {
				t.Fatal(e)
			}
			reader := fixtureReader()
			control := reader.Store.(fakeStore)
			control.release.HostPort = target.Port(number)
			reader.Store = healthStore{fakeStore: control, desired: policy.Desired{Health: policy.Health{Path: "/health", ExpectedStatus: 200}}}
			report, e := reader.Read(context.Background(), Request{App: "demo"})
			if e != nil {
				t.Fatal(e)
			}
			got := report.Apps[0].Health
			if got.Value == nil || *got.Value != (status == 200) {
				t.Fatal(got)
			}
		})
	}
}
func TestLiveRoutingUsesProvenOpaqueSource(t *testing.T) {
	reader := fixtureReader()
	s := fixtureSnapshot()
	s.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{{Name: caddySourceName("demo.caddy"), Domains: target.Known([]string{"demo.example.test"})}})
	reader.Inventory = fakeInventory{snapshot: s}
	report, e := reader.Read(context.Background(), Request{App: "demo"})
	if e != nil {
		t.Fatal(e)
	}
	if report.Host.LiveGeneration.Value == nil || *report.Host.LiveGeneration.Value != 2 || report.Apps[0].RoutePresent.Value == nil || !*report.Apps[0].RoutePresent.Value {
		t.Fatal(report)
	}
	s.LiveCaddyFiles.Value = &[]target.LiveCaddyFile{{Name: caddySourceName("demo.caddy"), Domains: target.Observation[[]string]{Status: target.Unknown}}}
	reader.Inventory = fakeInventory{snapshot: s}
	report, e = reader.Read(context.Background(), Request{App: "demo"})
	if e != nil {
		t.Fatal(e)
	}
	if report.Host.LiveGeneration.Value != nil || report.Apps[0].RoutePresent.Value != nil {
		t.Fatal("guessed live routing")
	}
}
func TestMalformedUnitOutputIsUnknown(t *testing.T) {
	for _, raw := range []string{"ActiveState=failed", "ActiveState=active\nSubState=running\nNRestarts=bad", "ActiveState=failed\nSubState=failed\nNRestarts=1\nNRestarts=2", "ActiveState=secret-value\nSubState=running\nNRestarts=1"} {
		if _, e := parseUnit("demo.service", raw); e == nil {
			t.Fatal("accepted malformed unit output")
		}
	}
}

type oversizedLogs struct{}

func (oversizedLogs) Read(context.Context, logs.Request) ([]logs.Line, error) {
	lines := make([]logs.Line, MaxLogLines+1)
	for i := range lines {
		lines[i] = logs.Line{Timestamp: "2026-10-09T00:00:00Z", Priority: 6, Message: "ready"}
	}
	return lines, nil
}
func TestOversizedTailIsUnknown(t *testing.T) {
	r := fixtureReader()
	r.Logs = oversizedLogs{}
	report, e := r.Read(context.Background(), Request{App: "demo"})
	if e != nil {
		t.Fatal(e)
	}
	if report.Apps[0].Logs.Reason != "logs_limit_exceeded" {
		t.Fatal(report.Apps[0].Logs)
	}
}

type memoryFS struct{}

func (memoryFS) ReadFile(context.Context, string) ([]byte, error) {
	return []byte("MemTotal: 200 kB\nMemAvailable: 123 kB\n"), nil
}
func (memoryFS) ReadDir(context.Context, string) ([]fs.DirEntry, error) { return nil, fs.ErrNotExist }
func (memoryFS) Readlink(context.Context, string) (string, error)       { return "", fs.ErrNotExist }
func TestMemoryAndPartialDiscovery(t *testing.T) {
	report, e := (Reader{Inventory: fakeInventory{snapshot: fixtureSnapshot()}, FS: memoryFS{}}).Read(context.Background(), Request{})
	if e != nil {
		t.Fatal(e)
	}
	if report.Host.MemoryAvailableBytes.Value == nil || *report.Host.MemoryAvailableBytes.Value != 123*1024 {
		t.Fatal(report.Host)
	}
	if report.AppNames.Status != "unknown" || report.AppNames.Reason != "app_discovery_incomplete" || len(report.Apps) != 1 {
		t.Fatal(report.AppNames)
	}
}

func TestUnattributedRouteIsNotGuessedAbsent(t *testing.T) {
	for _, domains := range []target.Observation[[]string]{target.Known([]string{"demo.example.test"}), {Status: target.Unknown}} {
		reader := fixtureReader()
		snapshot := fixtureSnapshot()
		snapshot.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{{Name: "file-unattributed", Domains: domains}})
		reader.Inventory = fakeInventory{snapshot: snapshot}
		report, e := reader.Read(context.Background(), Request{App: "demo"})
		if e != nil {
			t.Fatal(e)
		}
		if report.Apps[0].RoutePresent.Value != nil {
			t.Fatal("guessed absence from unattributed routes")
		}
		for _, finding := range report.Findings {
			if finding.Code == "route_missing" {
				t.Fatal("unproven route absence became a failure finding")
			}
		}
	}
}
func TestHealthyFactsDoNotMatchFailureRules(t *testing.T) {
	report := Report{Host: Host{FreeDiskBytes: Known(uint64(2)), MinimumFreeDiskBytes: Known(uint64(2)), Linger: Known(true)}, Apps: []App{{Name: "demo", Unit: Known(Unit{ActiveState: "active", Restarts: 2}), ContainerRunning: Known(true), Health: Known(true), Operations: Known([]RecentOperation{{State: ops.Succeeded}}), RoutePresent: Known(true), Drift: Known([]string{})}}}
	if got := Findings(report); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestReaderRetainedSecretsAndPartialDrift(t *testing.T) {
	for _, scenario := range []string{"retained_secrets", "unknown_image_changed_units", "absent_caddy", "unknown_image_absent_caddy"} {
		t.Run(scenario, func(t *testing.T) {
			r := fixtureReader()
			s := fixtureSnapshot()
			stored := r.Store.(fakeStore)
			stored.release.Units = append([]target.Unit{}, *(*s.Apps.Value)[0].QuadletUnits.Value...)
			a := &(*s.Apps.Value)[0]
			want := []string{}
			switch scenario {
			case "retained_secrets":
				stored.release.Secrets = []plan.SecretBinding{{VersionName: "brine.demo.key.v2", ID: "version-two"}}
				a.Secrets = target.Known([]target.Secret{{Name: "brine.demo.key.v1", ID: "version-one"}, {Name: "brine.demo.key.v2", ID: "version-two"}})
			case "unknown_image_changed_units":
				a.Image = target.Observation[target.Image]{Status: target.Unknown}
				a.QuadletUnits = target.Known([]target.Unit{{Name: "demo.container", Hash: "sha256:" + strings.Repeat("c", 64)}})
				want = []string{"units"}
			case "absent_caddy", "unknown_image_absent_caddy":
				s.CaddyConfig = target.Observation[target.CaddyConfigSet]{Status: target.Absent}
				if scenario == "unknown_image_absent_caddy" {
					a.Image = target.Observation[target.Image]{Status: target.Unknown}
				}
				want = []string{"caddy_file_missing"}
			}
			r.Store = stored
			r.Inventory = fakeInventory{snapshot: s}
			report, err := r.Read(context.Background(), Request{App: "demo"})
			if err != nil {
				t.Fatal(err)
			}
			got := report.Apps[0].Drift
			if len(want) == 0 {
				if got.Value != nil && len(*got.Value) != 0 {
					t.Fatalf("retained versions are expected leftovers: %+v", got)
				}
			} else if got.Value == nil || !reflect.DeepEqual(*got.Value, want) {
				t.Fatalf("proven drift must survive unrelated unknowns: got %+v, want %v", got, want)
			}
			found := false
			for _, finding := range report.Findings {
				if finding.Code == "artifact_drift" {
					found = true
				}
			}
			if found != (len(want) > 0) {
				t.Fatalf("artifact finding=%v, want %v", found, len(want) > 0)
			}
		})
	}
}

func TestDriftSharesAppScopedDomainEvidence(t *testing.T) {
	for _, scenario := range []string{"own_domains", "other_site", "unattributed", "desired_unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			s := fixtureSnapshot()
			r := fixtureReader().Store.(fakeStore).release
			r.Units = *(*s.Apps.Value)[0].QuadletUnits.Value
			file := target.LiveCaddyFile{App: "demo", Domains: target.Known([]string{"unexpected.invalid"})}
			desired := Known(policy.Desired{})
			switch scenario {
			case "other_site":
				file.App = "other"
			case "unattributed":
				file.App = ""
			case "desired_unavailable":
				desired = unknown[policy.Desired]("store_unavailable")
			}
			s.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{file})
			got := drift(s, "demo", r, desired)
			if scenario == "own_domains" {
				if got.Value == nil || !reflect.DeepEqual(*got.Value, []string{"domains"}) {
					t.Fatal(got)
				}
			} else if got.Status != "unknown" {
				t.Fatalf("must not guess this app's domains: %+v", got)
			}
		})
	}
}
