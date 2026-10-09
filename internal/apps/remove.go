package apps

import (
	"context"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
)

func (s Service) remove(ctx context.Context, app string) (ConfigPlan, error) {
	if !appPattern.MatchString(app) || s.Store == nil || s.LoadPolicy == nil {
		return ConfigPlan{}, result.New(result.DependencyMissing, nil)
	}
	locker, ok := s.Store.(interface {
		AcquireHostLock(context.Context) (ops.Lock, error)
	})
	if !ok {
		return ConfigPlan{}, result.New(result.DependencyMissing, nil)
	}
	lock, err := locker.AcquireHostLock(ctx)
	if err != nil {
		return ConfigPlan{}, err
	}
	defer lock.Release()
	pol, err := s.LoadPolicy(ctx)
	if err != nil {
		return ConfigPlan{}, err
	}
	snap, state, err := s.observed(ctx)
	if err != nil {
		return ConfigPlan{}, err
	}
	d := policy.Desired{SchemaVersion: 1, Name: spec.Name(app), Domains: []spec.Domain{}, Environment: []policy.Environment{}, Secrets: []policy.Secret{}}
	var image plan.Image
	for _, release := range state.Releases {
		if release.App == app {
			d = release.Desired
			image = release.Image
		}
	}
	d = RemovalDesired(d, pol)
	p, err := plan.BuildRemove(plan.Input{Desired: d, Snapshot: snap, State: state, Image: image})
	if err != nil {
		return ConfigPlan{}, err
	}
	return s.saveConfig(ctx, p, d)
}

// Removal authorization is app-scoped, not authorization to redeploy an old
// registry/domain/configuration under today's policy. Bind current policy only.
func RemovalDesired(d policy.Desired, p policy.Policy) policy.Desired {
	d.PolicyHash = p.Hash()
	d.PolicyVersion = p.Version()
	d.AppPorts = p.AppPorts()
	d.MinimumFreeDiskBytes = p.MinimumFreeDiskBytes()
	return d
}
