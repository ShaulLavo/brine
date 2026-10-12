package caddy

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

// ManagedIngressAuthority checks protected current records immediately before
// route publication/restoration. It does not mutate or release a fence.
type ManagedIngressAuthority interface {
	CheckManagedIngress(context.Context, ManagedIngressClaim) error
}

type ManagedIngressClaim struct {
	App           string
	BindingID     data.ReplicaBindingID
	EpochID       data.ReplicaEpochID
	RuntimeSHA256 string
	Pending       bool
	Accepts       []string
	Frontend      bool
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
		claim := ManagedIngressClaim{App: string(site.name), BindingID: managed.binding, EpochID: managed.epoch, RuntimeSHA256: managed.runtimeHash, Pending: managed.pending, Accepts: managed.desired.SpacetimeDB.Module.Accepts, Frontend: managed.appPort != 0}
		if err := managed.authority.CheckManagedIngress(ctx, claim); err != nil {
			return errors.New("caddy: managed ingress fenced")
		}
	}
	return ctx.Err()
}
