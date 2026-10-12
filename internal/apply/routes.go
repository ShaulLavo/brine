package apply

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

// GenerationRoutes delegates complete-candidate validation and reload-only
// publication to Caddy. Site must use the current verified operator policy.
type GenerationRoutes struct {
	Manager     *caddy.Manager
	Main        func(context.Context) ([]byte, error)
	Site        func(policy.Desired, target.Port) (caddy.Site, error)
	ManagedSite func(context.Context, policy.Desired, plan.ManagedServicePlan) (caddy.Site, error)
}

func (r GenerationRoutes) Publish(ctx context.Context, before caddy.State, p plan.Plan, d policy.Desired) (caddy.Result, error) {
	if r.Manager == nil || r.Main == nil || p.Managed == nil && r.Site == nil || p.Managed != nil && r.ManagedSite == nil {
		return caddy.Result{}, errors.New("apply: Caddy adapters required")
	}
	main, err := r.Main(ctx)
	if err != nil {
		return caddy.Result{}, err
	}
	var site caddy.Site
	if p.Managed != nil {
		site, err = r.ManagedSite(ctx, d, *p.Managed)
	} else {
		site, err = r.Site(d, p.HostPort)
	}
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

func (r GenerationRoutes) Withdraw(ctx context.Context, before caddy.State, app string) (caddy.Result, error) {
	if r.Manager == nil || r.Main == nil {
		return caddy.Result{}, errors.New("apply: Caddy adapters required")
	}
	main, err := r.Main(ctx)
	if err != nil {
		return caddy.Result{}, err
	}
	return r.Manager.Apply(ctx, main, before, caddy.Remove(spec.Name(app)))
}
func (r GenerationRoutes) SettleWithdrawal(ctx context.Context, before, observed caddy.State, app string) error {
	if r.Manager == nil || r.Main == nil {
		return errors.New("apply: Caddy adapters required")
	}
	main, err := r.Main(ctx)
	if err != nil {
		return err
	}
	return r.Manager.SettleWithdrawal(ctx, main, before, observed, spec.Name(app))
}

func (r GenerationRoutes) InspectManagedLoaded(ctx context.Context, observed caddy.State, p plan.Plan, d policy.Desired) error {
	if r.Manager == nil || r.ManagedSite == nil || p.Managed == nil {
		return errors.New("managed loaded inspection required")
	}
	site, err := r.ManagedSite(ctx, d, *p.Managed)
	if err != nil {
		return err
	}
	previous := caddy.State{Sites: map[string]caddy.Site{p.App + ".caddy": site}}
	return r.Manager.InspectLoadedChange(ctx, observed, previous)
}
