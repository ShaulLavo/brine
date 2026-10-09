package apply

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

// GenerationRoutes delegates complete-candidate validation and reload-only
// publication to Caddy. Site must use the current verified operator policy.
type GenerationRoutes struct {
	Manager *caddy.Manager
	Main    func(context.Context) ([]byte, error)
	Site    func(policy.Desired, target.Port) (caddy.Site, error)
}

func (r GenerationRoutes) Publish(ctx context.Context, before caddy.State, p plan.Plan, d policy.Desired) (caddy.Result, error) {
	if r.Manager == nil || r.Main == nil || r.Site == nil {
		return caddy.Result{}, errors.New("apply: Caddy adapters required")
	}
	main, err := r.Main(ctx)
	if err != nil {
		return caddy.Result{}, err
	}
	site, err := r.Site(d, p.HostPort)
	if err != nil {
		return caddy.Result{}, err
	}
	return r.Manager.Apply(ctx, main, before, caddy.Put(site))
}
func (r GenerationRoutes) Restore(ctx context.Context, installed, previous caddy.State) error {
	if r.Manager == nil || r.Main == nil {
		return errors.New("apply: Caddy adapters required")
	}
	main, err := r.Main(ctx)
	if err != nil {
		return err
	}
	return r.Manager.Restore(ctx, main, installed, previous)
}
