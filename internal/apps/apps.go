// Package apps reads committed application state and plans release rollbacks.
package apps

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type Store interface {
	LoadBrineState(context.Context, target.Identity, uint64) (plan.BrineState, error)
	PreviousRelease(context.Context, string) (ops.Release, error)
	ReleaseByID(context.Context, string, string) (ops.Release, error)
	LastOperation(context.Context, string) (ops.Operation, error)
	LoadPlan(context.Context, string) (plan.Plan, policy.Desired, error)
	SavePlan(context.Context, plan.Plan, policy.Desired) (string, error)
}
type Inventory interface {
	Collect(context.Context) (target.Snapshot, error)
}
type PersistentFacts interface {
	Collect(context.Context, policy.Desired) (target.Observation[[]target.PersistentDatabase], error)
}

type Service struct {
	Data       PersistentFacts
	Store      Store
	Inventory  Inventory
	Probe      HealthProbe
	LoadPolicy func(context.Context) (policy.Policy, error)
}
type Report struct {
	Apps []Status `json:"apps"`
}
type Release struct {
	ID          string        `json:"id"`
	PlanID      string        `json:"plan_id"`
	ImageDigest string        `json:"image_digest"`
	Port        target.Port   `json:"port"`
	Domains     []spec.Domain `json:"domains"`
}
type Health struct {
	UnitActive target.Observation[bool] `json:"unit_active"`
	Direct     string                   `json:"direct"`
}
type Drift struct {
	State  string   `json:"state"`
	Fields []string `json:"fields"`
}
type Status struct {
	App           string         `json:"app"`
	Current       Release        `json:"current"`
	Previous      *Release       `json:"previous"`
	LastOperation *ops.Operation `json:"last_operation"`
	Health        Health         `json:"health"`
	Drift         Drift          `json:"drift"`
}
type RollbackPlan struct {
	PlanID        string                  `json:"plan_id"`
	ReleaseID     string                  `json:"release_id"`
	Compatibility string                  `json:"compatibility"`
	Kind          plan.Kind               `json:"kind"`
	Diff          *plan.ConfigurationDiff `json:"diff"`
	Conflicts     []plan.Diagnostic       `json:"conflicts"`
}

func (s Service) observed(ctx context.Context) (target.Snapshot, plan.BrineState, error) {
	if s.Store == nil || s.Inventory == nil {
		return target.Snapshot{}, plan.BrineState{}, result.New(result.DependencyMissing, nil)
	}
	snap, err := s.Inventory.Collect(ctx)
	if err != nil {
		return snap, plan.BrineState{}, err
	}
	generation := uint64(0)
	if snap.Generation.Status == target.KnownStatus && snap.Generation.Value != nil {
		generation = *snap.Generation.Value
	}
	state, err := s.Store.LoadBrineState(ctx, snap.Identity, generation)
	return snap, state, err
}
func (s Service) releaseView(ctx context.Context, r ops.Release) (Release, error) {
	_, desired, err := s.Store.LoadPlan(ctx, r.PlanID)
	if err != nil {
		return Release{}, err
	}
	return Release{ID: r.ID, PlanID: r.PlanID, ImageDigest: r.Image.Digest, Port: r.HostPort, Domains: desired.Domains}, nil
}
func (s Service) Status(ctx context.Context, app string) (Report, error) {
	report := Report{Apps: []Status{}}
	snap, state, err := s.observed(ctx)
	if err != nil {
		return report, err
	}
	probes := 0
	for _, r := range state.Releases {
		if app != "" && app != r.App {
			continue
		}
		current, err := s.Store.ReleaseByID(ctx, r.App, r.ID)
		if err != nil {
			return report, err
		}
		item := Status{App: r.App, Current: Release{ID: r.ID, PlanID: current.PlanID, ImageDigest: r.Image.Digest, Port: r.HostPort, Domains: r.Desired.Domains}, Health: Health{UnitActive: target.Observation[bool]{Status: target.Unknown}, Direct: "not_checked"}}
		previous, err := s.Store.PreviousRelease(ctx, r.App)
		if err == nil {
			v, e := s.releaseView(ctx, previous)
			if e != nil {
				return report, e
			}
			item.Previous = &v
		} else if !errors.Is(err, store.ErrNotFound) {
			return report, err
		}
		op, err := s.Store.LastOperation(ctx, r.App)
		if err == nil {
			item.LastOperation = &op
		} else if !errors.Is(err, store.ErrNotFound) {
			return report, err
		}
		item.Drift = CompareDrift(snap, r)
		if snap.Apps.Status == target.KnownStatus && snap.Apps.Value != nil {
			for _, a := range *snap.Apps.Value {
				if a.Name == r.App && a.UnitActive != nil {
					item.Health.UnitActive = *a.UnitActive
				}
			}
		}
		if item.Drift.State != "drifted" && observedPortMatches(snap, r) && item.Health.UnitActive.Value != nil && *item.Health.UnitActive.Value && probes < DirectProbeLimit {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > DirectProbeTimeout {
				probe := s.Probe
				if probe == nil {
					probe = HTTPProbe{}
				}
				probes++
				probeCtx, cancel := context.WithTimeout(ctx, DirectProbeTimeout)
				healthy, err := probe.Check(probeCtx, r.HostPort, r.Desired.Health)
				cancel()
				item.Health.Direct = "unhealthy"
				if err == nil && healthy {
					item.Health.Direct = "healthy"
				}
			}
		}
		report.Apps = append(report.Apps, item)
	}
	if app != "" && len(report.Apps) == 0 {
		return report, result.New(result.AppNotFound, nil)
	}
	slices.SortFunc(report.Apps, func(a, b Status) int { return strings.Compare(a.App, b.App) })
	return report, nil
}

func (s Service) Rollback(ctx context.Context, app, id string) (RollbackPlan, error) {
	snap, state, err := s.observed(ctx)
	if err != nil {
		return RollbackPlan{}, err
	}
	var current *plan.CurrentRelease
	for i := range state.Releases {
		if state.Releases[i].App == app {
			current = &state.Releases[i]
			break
		}
	}
	if current == nil {
		return RollbackPlan{}, result.New(result.AppNotFound, nil)
	}
	var release ops.Release
	if id == "" {
		release, err = s.Store.PreviousRelease(ctx, app)
	} else {
		release, err = s.Store.ReleaseByID(ctx, app, id)
	}
	if errors.Is(err, store.ErrNotFound) {
		return RollbackPlan{}, result.New(result.ReleaseNotFound, nil)
	}
	if err != nil {
		return RollbackPlan{}, err
	}
	if release.ID == current.ID {
		return RollbackPlan{}, result.New(result.RollbackNoOp, nil)
	}
	stored, desired, err := s.Store.LoadPlan(ctx, release.PlanID)
	if err != nil {
		return RollbackPlan{}, err
	}
	if stored.App != app || stored.Target != snap.Identity || string(desired.Name) != app {
		return RollbackPlan{}, result.New(result.Conflict, nil)
	}
	if !stateless(current.Desired) || !stateless(desired) {
		return RollbackPlan{}, result.New(result.RecoveryRequired, nil)
	}
	p, err := plan.Build(plan.Input{Desired: desired, Snapshot: snap, Image: release.Image, State: state})
	if err != nil {
		return RollbackPlan{}, err
	}
	if p.Kind == plan.NoOp {
		return RollbackPlan{}, result.New(result.RollbackNoOp, nil)
	}
	planID, err := s.Store.SavePlan(ctx, p, desired)
	if err != nil {
		return RollbackPlan{}, err
	}
	return RollbackPlan{PlanID: planID, ReleaseID: release.ID, Compatibility: "stateless_compatible", Kind: p.Kind, Diff: p.Diff, Conflicts: p.Conflicts}, nil
}

func stateless(d policy.Desired) bool { return d.Stateless() }

// CompareDrift compares only this release's artifacts, preserving proven differences
// even when other observations are unavailable. Retained secret versions are allowed.
func CompareDrift(s target.Snapshot, r plan.CurrentRelease) Drift {
	d := Drift{State: "in_sync", Fields: []string{}}
	unknown := false
	check := func(field string, known, equal bool) {
		if !known {
			unknown = true
			return
		}
		if !equal {
			d.Fields = append(d.Fields, field)
		}
	}
	var observed *target.App
	if s.Apps.Status == target.KnownStatus && s.Apps.Value != nil {
		for i := range *s.Apps.Value {
			a := &(*s.Apps.Value)[i]
			if a.Name == r.App {
				observed = a
				break
			}
		}
		if observed == nil {
			d.Fields = append(d.Fields, "app")
		}
	} else {
		unknown = true
	}
	if observed != nil {
		a := observed
		check("image", a.Image.Status == target.Absent || a.Image.Status == target.KnownStatus && a.Image.Value != nil, a.Image.Value != nil && a.Image.Value.Digest == r.Image.Digest && a.Image.Value.Platform == r.Image.Platform)
		check("port", a.AllocatedHostPort.Status == target.Absent || a.AllocatedHostPort.Status == target.KnownStatus && a.AllocatedHostPort.Value != nil, a.AllocatedHostPort.Value != nil && *a.AllocatedHostPort.Value == r.HostPort)
		units := slices.Clone(r.Units)
		slices.SortFunc(units, func(a, b target.Unit) int { return strings.Compare(a.Name, b.Name) })
		live := []target.Unit{}
		if a.QuadletUnits.Value != nil {
			live = slices.Clone(*a.QuadletUnits.Value)
			slices.SortFunc(live, func(a, b target.Unit) int { return strings.Compare(a.Name, b.Name) })
		}
		check("units", a.QuadletUnits.Status == target.KnownStatus, reflect.DeepEqual(units, live))
		secrets := make([]target.Secret, 0, len(r.Secrets))
		for _, binding := range r.Secrets {
			secrets = append(secrets, target.Secret{Name: binding.VersionName, ID: binding.ID})
		}
		slices.SortFunc(secrets, func(a, b target.Secret) int { return strings.Compare(a.Name, b.Name) })
		liveSecrets := []target.Secret{}
		if a.Secrets.Value != nil {
			liveSecrets = slices.Clone(*a.Secrets.Value)
			slices.SortFunc(liveSecrets, func(a, b target.Secret) int { return strings.Compare(a.Name, b.Name) })
		}
		check("secrets", a.Secrets.Status == target.KnownStatus, containsSecrets(liveSecrets, secrets))
	}
	routeMatches := false
	if s.CaddyConfig.Value != nil {
		for _, file := range s.CaddyConfig.Value.Files {
			if file == r.CaddyFile {
				routeMatches = true
			}
		}
	}
	check("caddy", s.CaddyConfig.Status == target.KnownStatus || s.CaddyConfig.Status == target.Absent, routeMatches)
	domainsKnown := false
	domains := []string{}
	if s.LiveCaddyFiles.Status == target.KnownStatus && s.LiveCaddyFiles.Value != nil {
		for _, file := range *s.LiveCaddyFiles.Value {
			if file.App == r.App {
				if file.Domains.Status != target.KnownStatus || file.Domains.Value == nil {
					domainsKnown = false
					break
				}
				domainsKnown = true
				domains = append(domains, (*file.Domains.Value)...)
			}
		}
	}
	expectedDomains := make([]string, 0, len(r.Desired.Domains))
	for _, domain := range r.Desired.Domains {
		expectedDomains = append(expectedDomains, string(domain))
	}
	slices.Sort(domains)
	slices.Sort(expectedDomains)
	check("domains", domainsKnown, slices.Equal(domains, expectedDomains))
	if len(d.Fields) > 0 {
		d.State = "drifted"
	} else if unknown {
		d.State = "unknown"
	}
	slices.Sort(d.Fields)
	return d
}

func containsSecrets(live, expected []target.Secret) bool {
	for _, s := range expected {
		if !slices.Contains(live, s) {
			return false
		}
	}
	return true
}

func observedPortMatches(s target.Snapshot, r plan.CurrentRelease) bool {
	if s.Apps.Value == nil {
		return false
	}
	for _, app := range *s.Apps.Value {
		if app.Name == r.App {
			return app.AllocatedHostPort.Status == target.KnownStatus && app.AllocatedHostPort.Value != nil && *app.AllocatedHostPort.Value == r.HostPort
		}
	}
	return false
}
