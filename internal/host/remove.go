package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func (s Service) removalFacts(ctx context.Context, d policy.Desired) (apply.Facts, error) {
	if s.Store == nil || s.Inventory == nil || s.Policy == nil {
		return apply.Facts{}, result.New(result.DependencyMissing, nil)
	}
	pol, err := s.Policy.Load(ctx)
	if err != nil {
		return apply.Facts{}, err
	}
	snap, err := s.Inventory.Collect(ctx)
	if err != nil {
		return apply.Facts{}, err
	}
	if snap.Generation.Value == nil {
		return apply.Facts{}, result.New(result.Conflict, nil)
	}
	state, err := s.Store.LoadBrineState(ctx, snap.Identity, *snap.Generation.Value)
	if err != nil {
		return apply.Facts{}, err
	}
	d = apps.RemovalDesired(d, pol)
	var image plan.Image
	for _, release := range state.Releases {
		if release.App == string(d.Name) {
			image = release.Image
		}
	}
	out := apply.Facts{Input: plan.Input{Desired: d, Snapshot: snap, State: state, Image: image}, Routing: caddy.State{Files: map[string]string{}, Sites: map[string]caddy.Site{}}}
	for _, release := range state.Releases {
		site, err := caddy.NewSite(App(release.Desired), pol, spec.Port(release.HostPort))
		if err != nil {
			return out, err
		}
		out.Routing.Sites[release.CaddyFile.Name] = site
	}
	if snap.CaddyConfig.Status == target.KnownStatus && snap.CaddyConfig.Value != nil {
		out.Routing.Generation = snap.CaddyConfig.Value.Generation
		for _, file := range snap.CaddyConfig.Value.Files {
			out.Routing.Files[file.Name] = file.Hash
		}
	} else if snap.CaddyConfig.Status != target.Absent {
		return out, result.New(result.Conflict, nil)
	}
	return out, nil
}
