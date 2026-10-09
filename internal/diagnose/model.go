// Package diagnose gathers bounded read-only facts and applies deterministic findings.
package diagnose

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"github.com/ShaulLavo/brine/internal/target"
)

const MaxApps = 16
const MaxOperations = 10
const MaxReportBytes = 512 << 10
const LogBytes = 8 << 10
const MaxLogLines = 20
const TotalTimeout = 12 * time.Second
const ProbeTimeout = time.Second

var appName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type Request struct {
	App string `json:"app"`
}

func (r Request) Validate() error {
	if r.App != "" && !appName.MatchString(r.App) {
		return result.New(result.InvalidUsage, nil)
	}
	return nil
}
func DecodeRequest(raw []byte) (Request, error) {
	f, err := strictjson.Object(raw, "app")
	if err != nil {
		return Request{}, err
	}
	app, err := strictjson.Value[string](f["app"])
	if err != nil {
		return Request{}, err
	}
	r := Request{App: app}
	return r, r.Validate()
}

type Fact[T any] struct {
	Status string `json:"status"`
	Value  *T     `json:"value,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func Known[T any](value T) Fact[T]         { return Fact[T]{Status: "known", Value: &value} }
func unknown[T any](reason string) Fact[T] { return Fact[T]{Status: "unknown", Reason: reason} }
func observed[T any](o target.Observation[T]) Fact[T] {
	if o.Status == target.KnownStatus && o.Value != nil {
		return Known(*o.Value)
	}
	if o.Status == "" {
		return unknown[T]("inventory_unknown")
	}
	return unknown[T]("inventory_" + string(o.Status))
}

type Report struct {
	SchemaVersion int            `json:"schema_version"`
	Host          Host           `json:"host"`
	AppNames      Fact[[]string] `json:"app_names"`
	Apps          []App          `json:"apps"`
	Truncated     bool           `json:"truncated"`
	Findings      []Finding      `json:"findings"`
}
type Host struct {
	OS                   Fact[target.OS] `json:"os"`
	Arch                 Fact[string]    `json:"arch"`
	Versions             Fact[Versions]  `json:"versions"`
	FreeDiskBytes        Fact[uint64]    `json:"free_disk_bytes"`
	MinimumFreeDiskBytes Fact[uint64]    `json:"minimum_free_disk_bytes"`
	MemoryAvailableBytes Fact[uint64]    `json:"memory_available_bytes"`
	Runner               Fact[string]    `json:"runner"`
	Linger               Fact[bool]      `json:"linger"`
	SelectedGeneration   Fact[uint64]    `json:"selected_generation"`
	LiveGeneration       Fact[uint64]    `json:"live_generation"`
}
type Versions struct {
	Systemd    Fact[string] `json:"systemd"`
	Podman     Fact[string] `json:"podman"`
	Passt      Fact[string] `json:"passt"`
	Caddy      Fact[string] `json:"caddy"`
	Litestream Fact[string] `json:"litestream"`
}

func versions(v target.Versions) Versions {
	return Versions{Systemd: observed(v.Systemd), Podman: observed(v.Podman), Passt: observed(v.Passt), Caddy: observed(v.Caddy), Litestream: observed(v.Litestream)}
}

type Unit struct {
	Name        string `json:"name"`
	ActiveState string `json:"active_state"`
	SubState    string `json:"sub_state"`
	Restarts    uint64 `json:"restarts"`
}
type Release struct {
	ID              string `json:"id"`
	PlanID          string `json:"plan_id"`
	CaddyGeneration uint64 `json:"caddy_generation"`
}
type RecentOperation struct {
	ID          string    `json:"id"`
	State       ops.State `json:"state"`
	FailureCode string    `json:"failure_code,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type App struct {
	Name             string                  `json:"name"`
	Unit             Fact[Unit]              `json:"unit"`
	ContainerRunning Fact[bool]              `json:"container_running"`
	Health           Fact[bool]              `json:"health"`
	Operations       Fact[[]RecentOperation] `json:"operations"`
	CurrentRelease   Fact[Release]           `json:"current_release"`
	PreviousRelease  Fact[Release]           `json:"previous_release"`
	Drift            Fact[[]string]          `json:"drift"`
	Logs             Fact[[]logs.Line]       `json:"logs"`
	RoutePresent     Fact[bool]              `json:"route_present"`
}
type Finding struct {
	Code           string   `json:"code"`
	Severity       string   `json:"severity"`
	App            string   `json:"app,omitempty"`
	Message        string   `json:"message"`
	NextOperations []string `json:"next_operations"`
}

func DecodeReport(raw []byte) (Report, error) {
	bad := func() (Report, error) { return Report{}, result.New(result.TransportInvalidResponse, nil) }
	if len(raw) > MaxReportBytes {
		return bad()
	}
	fields, err := strictjson.Object(raw, "schema_version", "host", "app_names", "apps", "truncated", "findings")
	if err != nil {
		return bad()
	}
	for _, field := range fields {
		if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			return bad()
		}
	}
	var r Report
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || r.SchemaVersion != 1 || r.Apps == nil || r.Findings == nil || len(r.Apps) > MaxApps {
		return bad()
	}
	if !validFact(r.AppNames) || !validHost(r.Host) {
		return bad()
	}
	names := map[string]bool{}
	for i, a := range r.Apps {
		if !appName.MatchString(a.Name) || names[a.Name] || !validFact(a.Unit) || !validFact(a.ContainerRunning) || !validFact(a.Health) || !validFact(a.Operations) || !validFact(a.CurrentRelease) || !validFact(a.PreviousRelease) || !validFact(a.Drift) || !validFact(a.Logs) || !validFact(a.RoutePresent) {
			return bad()
		}
		names[a.Name] = true
		if a.Unit.Value != nil {
			u := *a.Unit.Value
			if u.Name != a.Name+".service" && u.Name != "brine-"+a.Name+".service" {
				return bad()
			}
			if _, err := parseUnit(u.Name, "ActiveState="+u.ActiveState+"\nSubState="+u.SubState+"\nNRestarts=0"); err != nil {
				return bad()
			}
		}
		for _, release := range []Fact[Release]{a.CurrentRelease, a.PreviousRelease} {
			if release.Value != nil {
				v := *release.Value
				if v.ID == "" || len(v.ID) > 256 || !jobs.ValidPlanID(v.PlanID) {
					return bad()
				}
			}
		}
		if a.Drift.Value != nil {
			if *a.Drift.Value == nil || len(*a.Drift.Value) > 8 {
				return bad()
			}
			for _, change := range *a.Drift.Value {
				if !slices.Contains([]string{"app_missing", "units", "image", "host_port", "secret_bindings", "caddy_file", "caddy_file_missing", "domains"}, change) {
					return bad()
				}
			}
		}
		if a.Operations.Value != nil {
			if *a.Operations.Value == nil || len(*a.Operations.Value) > MaxOperations {
				return bad()
			}
			for _, op := range *a.Operations.Value {
				if !ops.ValidState(op.State) || !jobs.ValidID(op.ID) || !validFailureCode(op.FailureCode) {
					return bad()
				}
			}
		}
		if a.Logs.Value != nil {
			b, _ := json.Marshal(*a.Logs.Value)
			if len(b) > LogBytes || len(*a.Logs.Value) > MaxLogLines {
				return bad()
			}
			lines, err := logs.DecodeLines(b)
			if err != nil {
				return bad()
			}
			r.Apps[i].Logs = Known(lines)
		}
	}
	if r.AppNames.Value != nil {
		if len(*r.AppNames.Value) != len(r.Apps) {
			return bad()
		}
		for _, name := range *r.AppNames.Value {
			if !names[name] {
				return bad()
			}
		}
	}
	expected := Findings(r)
	b, _ := json.Marshal(expected)
	actual, _ := json.Marshal(r.Findings)
	if !bytes.Equal(b, actual) {
		return bad()
	}
	return r, nil
}
func validFact[T any](f Fact[T]) bool {
	return f.Status == "known" && f.Value != nil && f.Reason == "" || (f.Status == "unknown" || f.Status == "absent") && f.Value == nil && reasonPattern.MatchString(f.Reason)
}

var reasonPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func validHost(h Host) bool {
	if h.Versions.Value != nil {
		v := *h.Versions.Value
		if !validFact(v.Systemd) || !validFact(v.Podman) || !validFact(v.Passt) || !validFact(v.Caddy) || !validFact(v.Litestream) {
			return false
		}
	}
	return validFact(h.OS) && validFact(h.Arch) && validFact(h.Versions) && validFact(h.FreeDiskBytes) && validFact(h.MinimumFreeDiskBytes) && validFact(h.MemoryAvailableBytes) && validFact(h.Runner) && validFact(h.Linger) && validFact(h.SelectedGeneration) && validFact(h.LiveGeneration)
}

func validFailureCode(code string) bool {
	if code == "" {
		return true
	}
	payload, _ := json.Marshal(ops.FailurePayload{Code: code})
	if ops.ValidateEvent(ops.Event{Kind: "failure", Payload: payload}) == nil {
		return true
	}
	payload, _ = json.Marshal(ops.StepPayload{Step: "preflight", Code: code, Outcome: "failed"})
	return ops.ValidateEvent(ops.Event{Kind: "step", Payload: payload}) == nil
}
