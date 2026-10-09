package diagnose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type Inventory interface {
	Collect(context.Context) (target.Snapshot, error)
}
type LogReader interface {
	Read(context.Context, logs.Request) ([]logs.Line, error)
}
type ControlStore interface {
	AppNames(context.Context) ([]string, error)
	RecentOperations(context.Context, string, int) ([]ops.OperationRecord, error)
	CurrentRelease(context.Context, string) (ops.Release, error)
	PreviousRelease(context.Context, string) (ops.Release, error)
	LoadPlan(context.Context, string) (plan.Plan, policy.Desired, error)
}

type Reader struct {
	Inventory            Inventory
	Logs                 LogReader
	Store                ControlStore
	Runner               localexec.StdoutRunner
	FS                   inventory.FileSystem
	MinimumFreeDiskBytes func(context.Context) (uint64, error)
	Timeout              time.Duration
}

// A dependency that ignores cancellation can occupy one slot, not unbounded goroutines.
var probeSlots = make(chan struct{}, 4)

func bounded[T any](ctx context.Context, timeout time.Duration, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
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
	case r := <-done:
		return r.value, r.err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
func reason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "probe_timeout"
	}
	var safe *result.Error
	if errors.As(err, &safe) {
		return string(safe.Code())
	}
	return "probe_failed"
}
func fact[T any](ctx context.Context, fn func(context.Context) (T, error)) Fact[T] {
	v, err := bounded(ctx, ProbeTimeout, fn)
	if err != nil {
		return unknown[T](reason(err))
	}
	return Known(v)
}
func (r Reader) Read(parent context.Context, request Request) (Report, error) {
	if err := request.Validate(); err != nil {
		return Report{}, err
	}
	timeout := r.Timeout
	if timeout <= 0 || timeout > TotalTimeout {
		timeout = TotalTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	report := Report{SchemaVersion: 1, Apps: []App{}, Findings: []Finding{}, AppNames: unknown[[]string]("inventory_unavailable")}
	h := Host{OS: unknown[target.OS]("inventory_unavailable"), Arch: unknown[string]("inventory_unavailable"), Versions: unknown[Versions]("inventory_unavailable"), FreeDiskBytes: unknown[uint64]("inventory_unavailable"), MinimumFreeDiskBytes: unknown[uint64]("policy_unavailable"), MemoryAvailableBytes: unknown[uint64]("probe_unavailable"), Runner: unknown[string]("inventory_unavailable"), Linger: unknown[bool]("inventory_unavailable"), SelectedGeneration: unknown[uint64]("inventory_unavailable"), LiveGeneration: unknown[uint64]("live_generation_not_exposed")}
	snapshot := target.Snapshot{}
	if r.Inventory != nil {
		s, err := bounded(ctx, 4*time.Second, r.Inventory.Collect)
		if err == nil {
			snapshot = s
			if s.OS.ID != "" && s.OS.Version != "" {
				h.OS = Known(s.OS)
			}
			if s.Arch != "" {
				h.Arch = Known(s.Arch)
			}
			h.Versions = Known(versions(s.Versions))
			h.FreeDiskBytes = observed(s.FreeDiskBytes)
			h.Runner = observed(s.Runner.User)
			h.Linger = observed(s.Runner.Linger)
			if s.CaddyConfig.Status == target.KnownStatus && s.CaddyConfig.Value != nil {
				h.SelectedGeneration = Known(s.CaddyConfig.Value.Generation)
			}
			if s.Apps.Value != nil {
				names := []string{}
				for _, a := range *s.Apps.Value {
					names = append(names, a.Name)
				}
				report.AppNames = Known(names)
			}
		} else {
			h.OS = unknown[target.OS](reason(err))
			h.Arch = unknown[string](reason(err))
		}
	}
	if r.MinimumFreeDiskBytes != nil {
		h.MinimumFreeDiskBytes = fact(ctx, r.MinimumFreeDiskBytes)
	}
	if r.FS != nil {
		h.MemoryAvailableBytes = fact(ctx, func(ctx context.Context) (uint64, error) {
			data, err := r.FS.ReadFile(ctx, "/proc/meminfo")
			if err != nil {
				return 0, err
			}
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 3 && fields[0] == "MemAvailable:" && fields[2] == "kB" {
					n, e := strconv.ParseUint(fields[1], 10, 54)
					return n * 1024, e
				}
			}
			return 0, errors.New("memory unavailable")
		})
	}
	h.LiveGeneration = liveGeneration(snapshot)
	report.Host = h
	if r.Logs == nil {
		if executor, ok := r.Runner.(localexec.Executor); ok {
			r.Logs = logs.Reader{Inventory: snapshotInventory{snapshot}, Executor: executor}
		}
	}
	names := []string{}
	inventoryNamesKnown := report.AppNames.Value != nil
	storeNamesKnown := false
	if report.AppNames.Value != nil {
		names = append(names, (*report.AppNames.Value)...)
	}
	if r.Store != nil && request.App == "" {
		stored := fact(ctx, r.Store.AppNames)
		if stored.Value != nil {
			storeNamesKnown = true
			names = append(names, (*stored.Value)...)
		}
		if report.AppNames.Value == nil && stored.Value != nil {
			report.AppNames = stored
		}
	}
	if request.App != "" {
		names = []string{request.App}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) > MaxApps {
		names = names[:MaxApps]
		report.Truncated = true
	}
	if request.App != "" || inventoryNamesKnown && storeNamesKnown {
		report.AppNames = Known(slices.Clone(names))
	} else {
		report.AppNames = unknown[[]string]("app_discovery_incomplete")
	}
	for _, name := range names {
		if !appName.MatchString(name) {
			continue
		}
		report.Apps = append(report.Apps, r.app(ctx, snapshot, name))
	}
	report.Findings = Findings(report)
	if err := parent.Err(); err != nil {
		return Report{}, err
	}
	raw, err := json.Marshal(report)
	if err != nil || len(raw) > MaxReportBytes {
		return Report{}, errors.New("diagnostic report size exceeded")
	}
	return report, nil
}
func emptyApp(name string) App {
	return App{Name: name, Unit: unknown[Unit]("ownership_unknown"), ContainerRunning: unknown[bool]("ownership_unknown"), Health: unknown[bool]("release_unknown"), Operations: unknown[[]RecentOperation]("store_unavailable"), CurrentRelease: unknown[Release]("store_unavailable"), PreviousRelease: unknown[Release]("store_unavailable"), Drift: unknown[[]string]("release_unknown"), Logs: unknown[[]logs.Line]("log_reader_unavailable"), RoutePresent: unknown[bool]("inventory_unknown")}
}
func (r Reader) app(ctx context.Context, s target.Snapshot, name string) App {
	a := emptyApp(name)
	var observedApp *target.App
	if s.Apps.Value != nil {
		for _, entry := range *s.Apps.Value {
			if entry.Name == name {
				copy := entry
				observedApp = &copy
			}
		}
	}
	if s.LiveCaddyFiles.Value != nil {
		present := false
		uncertain := false
		for _, file := range *s.LiveCaddyFiles.Value {
			if file.App != name && file.Name != caddySourceName(name+".caddy") {
				if file.App == "" && (file.Domains.Value == nil || len(*file.Domains.Value) > 0) {
					uncertain = true
				}
				continue
			}
			if file.Domains.Value == nil {
				uncertain = true
			} else if len(*file.Domains.Value) > 0 {
				present = true
			}
		}
		if present || !uncertain {
			a.RoutePresent = Known(present)
		}
	}
	unit := ""
	if observedApp != nil && observedApp.QuadletUnits.Value != nil {
		for _, u := range *observedApp.QuadletUnits.Value {
			if u.Name == name+".container" || u.Name == "brine-"+name+".container" {
				if unit != "" {
					unit = ""
					break
				}
				unit = strings.TrimSuffix(u.Name, ".container")
			}
		}
	}
	if r.Runner != nil && unit != "" {
		a.Unit = fact(ctx, func(ctx context.Context) (Unit, error) {
			out, e := r.Runner.RunStdout(ctx, "systemctl", "--user", "show", unit+".service", "--property=ActiveState,SubState,NRestarts", "--no-pager")
			if e != nil {
				return Unit{}, e
			}
			return parseUnit(unit+".service", out)
		})
		a.ContainerRunning = fact(ctx, func(ctx context.Context) (bool, error) {
			out, e := r.Runner.RunStdout(ctx, "podman", "--remote=false", "inspect", "--type", "container", "--format", `{"name":{{json .Name}},"running":{{json .State.Running}},"unit":{{json (index .Config.Labels "PODMAN_SYSTEMD_UNIT")}}}`, "systemd-"+unit)
			if e != nil {
				return false, e
			}
			var v struct {
				Name    string
				Running *bool
				Unit    string
			}
			if len(out) >= localexec.OutputLimit || json.Unmarshal([]byte(out), &v) != nil || v.Name != "systemd-"+unit || v.Unit != unit+".service" || v.Running == nil {
				return false, errors.New("container observation invalid")
			}
			return *v.Running, nil
		})
	}
	if r.Store != nil {
		a.Operations = fact(ctx, func(ctx context.Context) ([]RecentOperation, error) {
			records, e := r.Store.RecentOperations(ctx, name, MaxOperations)
			if e != nil {
				return nil, e
			}
			if len(records) > MaxOperations {
				return nil, errors.New("operation size exceeded")
			}
			out := []RecentOperation{}
			for _, v := range records {
				out = append(out, RecentOperation{ID: v.Operation.ID, State: v.Operation.State, FailureCode: v.FailureCode, UpdatedAt: v.Operation.UpdatedAt})
			}
			return out, nil
		})
		current, e := bounded(ctx, ProbeTimeout, func(ctx context.Context) (ops.Release, error) { return r.Store.CurrentRelease(ctx, name) })
		a.CurrentRelease = releaseFact(current, e)
		previous, e := bounded(ctx, ProbeTimeout, func(ctx context.Context) (ops.Release, error) { return r.Store.PreviousRelease(ctx, name) })
		a.PreviousRelease = releaseFact(previous, e)
		if a.CurrentRelease.Value != nil {
			desired := fact(ctx, func(ctx context.Context) (policy.Desired, error) {
				_, d, e := r.Store.LoadPlan(ctx, current.PlanID)
				return d, e
			})
			a.Drift = drift(s, name, current, desired)
			if desired.Value == nil {
				a.Health = unknown[bool](desired.Reason)
			}
			if desired.Value != nil && unit != "" {
				a.Health = fact(ctx, func(ctx context.Context) (bool, error) {
					d := *desired.Value
					transport := http.DefaultTransport.(*http.Transport).Clone()
					transport.Proxy = nil
					defer transport.CloseIdleConnections()
					client := http.Client{Transport: transport, Timeout: ProbeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
					url := fmt.Sprintf("http://127.0.0.1:%d%s", current.HostPort, d.Health.Path)
					req, e := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
					if e != nil {
						return false, e
					}
					response, e := client.Do(req)
					if e != nil {
						return false, e
					}
					defer response.Body.Close()
					_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
					return response.StatusCode == d.Health.ExpectedStatus, nil
				})
			}
		}
	}
	if r.Logs != nil {
		a.Logs = fact(ctx, func(ctx context.Context) ([]logs.Line, error) {
			lines, e := r.Logs.Read(ctx, logs.Request{App: name, Tail: MaxLogLines})
			if e != nil {
				return nil, e
			}
			raw, e := json.Marshal(lines)
			if e != nil || len(raw) > LogBytes || len(lines) > MaxLogLines {
				return nil, result.New(result.LogsLimitExceeded, nil)
			}
			return logs.DecodeLines(raw)
		})
	}
	return a
}
func releaseFact(r ops.Release, err error) Fact[Release] {
	if errors.Is(err, store.ErrNotFound) {
		return Fact[Release]{Status: "absent", Reason: "no_committed_release"}
	}
	if err != nil {
		return unknown[Release](reason(err))
	}
	return Known(Release{ID: r.ID, PlanID: r.PlanID, CaddyGeneration: r.CaddyGeneration})
}
func parseUnit(name, out string) (Unit, error) {
	if len(out) >= localexec.OutputLimit {
		return Unit{}, errors.New("unit output too large")
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return Unit{}, errors.New("unit output invalid")
		}
		if _, exists := fields[k]; exists {
			return Unit{}, errors.New("duplicate unit property")
		}
		fields[k] = v
	}
	if !slices.Contains([]string{"active", "reloading", "inactive", "failed", "activating", "deactivating", "maintenance", "refreshing"}, fields["ActiveState"]) || !appName.MatchString(fields["SubState"]) {
		return Unit{}, errors.New("unit state invalid")
	}
	n, e := strconv.ParseUint(fields["NRestarts"], 10, 64)
	return Unit{Name: name, ActiveState: fields["ActiveState"], SubState: fields["SubState"], Restarts: n}, e
}
func drift(s target.Snapshot, name string, r ops.Release, desired Fact[policy.Desired]) Fact[[]string] {
	current := plan.CurrentRelease{App: name, ID: r.ID, Image: r.Image, HostPort: r.HostPort, Secrets: r.Secrets, Units: r.Units, CaddyFile: r.CaddyFile}
	if desired.Value != nil {
		current.Desired = *desired.Value
	} else {
		// Without the committed desired domains, no live-domain comparison is justified.
		s.LiveCaddyFiles = target.Observation[[]target.LiveCaddyFile]{Status: target.Unknown}
	}
	comparison := apps.CompareDrift(s, current)
	if comparison.State == "unknown" {
		return unknown[[]string]("artifact_observation_unknown")
	}
	changes := []string{}
	for _, field := range comparison.Fields {
		switch field {
		case "app":
			changes = append(changes, "app_missing")
		case "port":
			changes = append(changes, "host_port")
		case "secrets":
			changes = append(changes, "secret_bindings")
		case "caddy":
			code := "caddy_file"
			if s.CaddyConfig.Status == target.Absent {
				code = "caddy_file_missing"
			} else if s.CaddyConfig.Value != nil {
				found := false
				for _, file := range s.CaddyConfig.Value.Files {
					found = found || file.Name == r.CaddyFile.Name
				}
				if !found {
					code = "caddy_file_missing"
				}
			}
			changes = append(changes, code)
		default:
			changes = append(changes, field)
		}
	}
	return Known(changes)
}

// DiskStore opens existing state read-only for each bounded query. It never enrolls or migrates a host.
type DiskStore struct{ Dir string }

func (d DiskStore) open(ctx context.Context) (*store.Store, error) {
	return store.OpenReadOnly(ctx, filepath.Clean(d.Dir))
}
func (d DiskStore) AppNames(ctx context.Context) ([]string, error) {
	s, e := d.open(ctx)
	if e != nil {
		return nil, e
	}
	defer s.Close()
	return s.AppNames(ctx)
}
func (d DiskStore) RecentOperations(ctx context.Context, app string, limit int) ([]ops.OperationRecord, error) {
	s, e := d.open(ctx)
	if e != nil {
		return nil, e
	}
	defer s.Close()
	return s.RecentOperations(ctx, app, limit)
}
func (d DiskStore) CurrentRelease(ctx context.Context, app string) (ops.Release, error) {
	s, e := d.open(ctx)
	if e != nil {
		return ops.Release{}, e
	}
	defer s.Close()
	return s.CurrentRelease(ctx, app)
}
func (d DiskStore) PreviousRelease(ctx context.Context, app string) (ops.Release, error) {
	s, e := d.open(ctx)
	if e != nil {
		return ops.Release{}, e
	}
	defer s.Close()
	return s.PreviousRelease(ctx, app)
}
func (d DiskStore) LoadPlan(ctx context.Context, id string) (plan.Plan, policy.Desired, error) {
	s, e := d.open(ctx)
	if e != nil {
		return plan.Plan{}, policy.Desired{}, e
	}
	defer s.Close()
	return s.LoadPlan(ctx, id)
}

type snapshotInventory struct{ snapshot target.Snapshot }

func (s snapshotInventory) Collect(context.Context) (target.Snapshot, error) { return s.snapshot, nil }

func caddySourceName(file string) string {
	sum := sha256.Sum256([]byte("/etc/caddy/brine/current/" + file))
	return "file-" + hex.EncodeToString(sum[:])
}
func liveGeneration(s target.Snapshot) Fact[uint64] {
	unknownGeneration := unknown[uint64]("live_generation_unproven")
	if s.CaddyConfig.Value == nil || s.LiveCaddyFiles.Value == nil || len(s.CaddyConfig.Value.Files) == 0 {
		return unknownGeneration
	}
	// Inventory emits live sources only after disk-adapted and active configurations match.
	for _, selected := range s.CaddyConfig.Value.Files {
		found := false
		for _, live := range *s.LiveCaddyFiles.Value {
			if live.Name == caddySourceName(selected.Name) && live.Domains.Value != nil {
				found = true
				break
			}
		}
		if !found {
			return unknownGeneration
		}
	}
	return Known(s.CaddyConfig.Value.Generation)
}
