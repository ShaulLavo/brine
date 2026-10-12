package host

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

func (s Service) managedFacts(ctx context.Context, desired policy.Desired, pol policy.Policy, snapshot target.Snapshot, state plan.BrineState) (apply.Facts, error) {
	platform := target.Platform{OS: "linux", Arch: snapshot.Arch}
	engineImage, err := s.Images.Resolve(ctx, desired.SpacetimeDB.Image, platform)
	if err != nil {
		return apply.Facts{}, err
	}
	primary := engineImage
	if desired.App().HasAppContainer() {
		primary, err = s.Images.Resolve(ctx, desired.Image, platform)
		if err != nil {
			return apply.Facts{}, err
		}
	}
	managed := &plan.ManagedFacts{EngineImage: engineImage, Fences: []data.QuiescenceFence{}, NativeState: target.Observation[string]{Status: target.Unknown}, IngressProtected: target.Observation[bool]{Status: target.Unknown}}
	projection, err := s.Store.ReadManagedApp(ctx, string(desired.Name))
	if err == nil {
		database, archive := projection.Reserved.Database, projection.Reserved.Archive
		if database.Name != desired.SpacetimeDB.Name || database.Root != desired.SpacetimeDB.PersistentRoot || archive.Destination.Reference != desired.SpacetimeDB.BackupDestination {
			return apply.Facts{}, result.New(result.Conflict, nil)
		}
		managed.Database, managed.Archive = &database, &archive
		if projection.Permit == nil {
			preparation, readErr := s.Store.ReadManagedPreparation(ctx, database.BindingID)
			if readErr == nil {
				preparedPlan, preparedDesired, loadErr := s.Store.LoadPlan(ctx, preparation.PlanID)
				if loadErr != nil || preparedPlan.Managed == nil {
					return apply.Facts{}, errors.Join(store.ErrInvalid, loadErr)
				}
				frozen := preparedPlan.Managed.Clone()
				managed.Preparation = &frozen
				managed.Prepared = target.Observation[bool]{Status: target.Unknown}
				if s.managedPreparation != nil {
					complete, inspectErr := s.managedPreparation.ManagedPrepared(ctx, preparation.OperationID, preparedPlan, preparedDesired)
					if inspectErr == nil {
						managed.Prepared = target.Known(complete)
					}
				}
			} else if !errors.Is(readErr, store.ErrNotFound) {
				return apply.Facts{}, readErr
			}
		}
		if projection.Permit != nil {
			runtime := projection.Permit.Runtime.Clone()
			managed.Runtime = &runtime
			managed.Fences = projection.Permit.Fences
			if runtime.State == data.EngineInitialized && s.managedObserver != nil {
				fingerprint, hashErr := managedRuntimeHash(runtime)
				if hashErr != nil {
					return apply.Facts{}, hashErr
				}
				proof, observeErr := s.managedObserver.current(ctx, runtime.BindingID, fingerprint)
				if observeErr == nil && !proof.observedAt.IsZero() {
					managed.NativeState = target.Known("running")
				} else if s.managedObserver.stopped(ctx, *projection.Permit) == nil {
					managed.NativeState = target.Known("stopped")
				}
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return apply.Facts{}, err
	}
	routing, err := managedRoutingFacts(state, pol, snapshot)
	if err != nil {
		return apply.Facts{}, err
	}
	return apply.Facts{Input: plan.Input{Desired: desired, Snapshot: snapshot, State: state, Image: primary, Managed: managed}, Routing: routing}, nil
}

func managedRoutingFacts(state plan.BrineState, pol policy.Policy, snapshot target.Snapshot) (caddy.State, error) {
	routing := caddy.State{Files: map[string]string{}, Sites: map[string]caddy.Site{}}
	for _, release := range state.Releases {
		var site caddy.Site
		var err error
		if release.Managed != nil {
			site, err = caddy.CommittedManagedSite(release.Desired, *release.Managed)
		} else {
			site, err = caddy.NewSite(release.Desired.App(), pol, spec.Port(release.HostPort))
		}
		if err != nil {
			return caddy.State{}, err
		}
		routing.Files[release.CaddyFile.Name] = release.CaddyFile.Hash
		routing.Sites[release.CaddyFile.Name] = site
	}
	if snapshot.CaddyConfig.Status == target.KnownStatus && snapshot.CaddyConfig.Value != nil {
		routing.Generation = snapshot.CaddyConfig.Value.Generation
	} else if snapshot.CaddyConfig.Status != target.Absent {
		return caddy.State{}, result.New(result.Conflict, nil)
	}
	return routing, nil
}
