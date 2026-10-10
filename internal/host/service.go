// Package host composes the enrolled deployment engine without presentation code.
package host

import (
	"bytes"
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type PolicyLoader interface {
	Load(context.Context) (policy.Policy, error)
}
type ImageResolver interface {
	Resolve(context.Context, spec.ImageReference, target.Platform) (plan.Image, error)
}
type Service struct {
	Data      PersistentFacts
	Store     *store.Store
	Inventory dispatch.Inventory
	Policy    PolicyLoader
	Images    ImageResolver
	Requester string
}

func (s Service) Authorize(ctx context.Context, class dispatch.Class) error {
	if s.Requester == "" {
		return result.New(result.DispatchOperationRefused, nil)
	}
	if s.Store == nil {
		return result.New(result.DependencyMissing, nil)
	}
	if class == dispatch.Mutating {
		if s.Policy == nil {
			return result.New(result.DependencyMissing, nil)
		}
		if _, err := s.Policy.Load(ctx); err != nil {
			var refused *policy.Refusal
			if errors.As(err, &refused) {
				return err
			}
			return result.New(result.DependencyMissing, err)
		}
	}
	return nil
}

func (s Service) Plan(ctx context.Context, app spec.App) (dispatch.Planned, error) {
	if s.Requester == "" {
		return dispatch.Planned{}, result.New(result.DispatchOperationRefused, nil)
	}
	if s.Store == nil || s.Inventory == nil || s.Policy == nil || s.Images == nil {
		return dispatch.Planned{}, result.New(result.DependencyMissing, nil)
	}
	lock, err := s.Store.AcquireHostLock(ctx)
	if err != nil {
		return dispatch.Planned{}, err
	}
	defer lock.Release()
	facts, err := s.facts(ctx, app)
	if err != nil {
		return dispatch.Planned{}, err
	}
	p, err := plan.Build(facts.Input)
	if err != nil {
		return dispatch.Planned{}, err
	}
	id, err := s.Store.SavePlan(ctx, p, facts.Input.Desired)
	return dispatch.Planned{PlanID: id, Kind: p.Kind, Diff: p.Diff, Conflicts: p.Conflicts}, err
}

func (s Service) facts(ctx context.Context, app spec.App) (apply.Facts, error) {
	var out apply.Facts
	if s.Store == nil || s.Inventory == nil || s.Policy == nil || s.Images == nil {
		return out, result.New(result.DependencyMissing, nil)
	}
	pol, err := s.Policy.Load(ctx)
	if err != nil {
		var refused *policy.Refusal
		if errors.As(err, &refused) {
			return out, err
		}
		return out, result.New(result.DependencyMissing, err)
	}
	d, err := policy.Normalize(app, pol)
	if err != nil {
		return out, err
	}
	snap, err := s.Inventory.Collect(ctx)
	if err != nil {
		return out, err
	}
	if snap.Generation.Status != target.KnownStatus || snap.Generation.Value == nil {
		return out, result.New(result.Conflict, nil)
	}
	state, err := s.Store.LoadBrineState(ctx, snap.Identity, *snap.Generation.Value)
	if err != nil {
		return out, err
	}
	image, err := s.Images.Resolve(ctx, d.Image, target.Platform{OS: "linux", Arch: snap.Arch})
	if err != nil {
		return out, err
	}
	if len(d.Databases) > 0 {
		observation := target.Observation[[]target.PersistentDatabase]{Status: target.Unknown}
		if s.Data != nil {
			observation, err = s.Data.Collect(ctx, d)
			if err != nil {
				return out, err
			}
		}
		snap.PersistentData = &observation
	}
	out.Input = plan.Input{Desired: d, Snapshot: snap, State: state, Image: image}
	out.Routing = caddy.State{Files: map[string]string{}, Sites: map[string]caddy.Site{}}
	for _, release := range state.Releases {
		site, err := caddy.NewSite(App(release.Desired), pol, spec.Port(release.HostPort))
		if err != nil {
			return out, err
		}
		out.Routing.Files[release.CaddyFile.Name] = release.CaddyFile.Hash
		out.Routing.Sites[release.CaddyFile.Name] = site
	}
	if snap.CaddyConfig.Status == target.KnownStatus && snap.CaddyConfig.Value != nil {
		out.Routing.Generation = snap.CaddyConfig.Value.Generation
	} else if snap.CaddyConfig.Status != target.Absent {
		return out, errors.New("host: unknown routing generation")
	}
	return out, nil
}

type Executor struct {
	Service Service
	Engine  apply.Executor
}

func (e Executor) Run(ctx context.Context, id string, p plan.Plan, d policy.Desired) error {
	if p.Lifecycle == plan.PrepareData {
		return e.runDataPreparation(ctx, id, p, d)
	}
	if p.Lifecycle == plan.RemoveApp {
		engine := e.Engine
		engine.Facts = operationFacts{service: e.Service, desired: d, removal: true}
		return engine.Run(ctx, id, p, d)
	}
	if p.Image.ManifestDigest.Status != target.KnownStatus || p.Image.ManifestDigest.Value == nil {
		return result.New(result.Conflict, nil)
	}
	facts, err := e.Service.facts(ctx, App(d))
	if err != nil {
		return result.New(result.Conflict, err)
	}
	var fresh plan.Plan
	if p.Lifecycle != "" {
		fresh, err = plan.BuildLifecycle(facts.Input, p.Lifecycle)
	} else {
		fresh, err = plan.Build(facts.Input)
	}
	if err != nil {
		return result.New(result.Conflict, err)
	}
	original, err := p.CanonicalBytes()
	if err != nil {
		return result.New(result.Conflict, err)
	}
	rebuilt, err := fresh.CanonicalBytes()
	if err != nil || !bytes.Equal(original, rebuilt) {
		return result.New(result.Conflict, errors.Join(err, ops.RecordPlanDrift(ctx, e.Service.Store, id, p, fresh)))
	}
	engine := e.Engine
	engine.Facts = fixedFacts{facts}
	return engine.Run(ctx, id, p, d)
}

type fixedFacts struct{ facts apply.Facts }

func (f fixedFacts) Read(context.Context) (apply.Facts, error) { return f.facts, nil }

// App reconstructs the strict spec input, not a policy authorization.
func App(d policy.Desired) spec.App { return d.App() }

type releases struct{ *store.Store }

func (r releases) CurrentRelease(ctx context.Context, app string) (apply.Release, bool, error) {
	release, err := r.Store.CurrentRelease(ctx, app)
	if errors.Is(err, store.ErrNotFound) {
		return release, false, nil
	}
	return release, err == nil, err
}
