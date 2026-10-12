package caddy

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

// ManagedIngressAuthority checks protected current records immediately before
// route publication/restoration. It does not mutate or release a fence.
type ManagedIngressAuthority interface {
	CheckManagedIngress(context.Context, string, data.ReplicaBindingID, data.ReplicaEpochID, string) error
}

func WithManagedAuthority(site Site, authority ManagedIngressAuthority) (Site, error) {
	if site.managed == nil || authority == nil {
		return Site{}, errors.New("caddy: managed ingress authority required")
	}
	copy := *site.managed
	copy.authority = authority
	site.managed = &copy
	return site, nil
}

func admitManagedSites(ctx context.Context, sites map[string]Site) error {
	for _, site := range sites {
		if site.managed == nil {
			continue
		}
		managed := site.managed
		if managed.authority == nil {
			return errors.New("caddy: managed ingress authority unavailable")
		}
		if err := managed.authority.CheckManagedIngress(ctx, string(site.name), managed.binding, managed.epoch, managed.runtimeHash); err != nil {
			return errors.New("caddy: managed ingress fenced")
		}
	}
	return ctx.Err()
}
