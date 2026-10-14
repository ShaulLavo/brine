package host

import (
	"context"
	"errors"
	"maps"

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
	managed := &plan.ManagedFacts{EngineImage: engineImage, Fences: []data.QuiescenceFence{}, NativeState: target.Observation[string]{Status: target.Unknown}, LoggingPrerequisite: target.Observation[bool]{Status: target.Unknown}}
	if s.managedLoggingFacts != nil {
		loaded, rootHash, configHash, readErr := s.managedLoggingFacts(ctx)
		if readErr == nil {
			managed.LoggingRootSHA256 = rootHash
			managed.LoggingConfigSHA256 = configHash
			managed.LoggingPrerequisite = target.Known(caddy.VerifyProspectiveManagedLogging(loaded, desired.Domains) == nil)
		}
	}
	projection, err := s.Store.ReadManagedApp(ctx, string(desired.Name))
	if err == nil {
		database, archive := projection.Reserved.Database, projection.Reserved.Archive
		if database.Name != desired.SpacetimeDB.Name || database.Root != desired.SpacetimeDB.PersistentRoot || archive.Destination.Reference != desired.SpacetimeDB.BackupDestination {
			return apply.Facts{}, result.New(result.Conflict, nil)
		}
		managed.Database, managed.Archive = &database, &archive
		route, routeErr := s.Store.ReadManagedRouteState(ctx, string(desired.Name))
		if routeErr == nil {
			managed.RouteRevision = route.Revision
		} else if !errors.Is(routeErr, store.ErrNotFound) {
			return apply.Facts{}, routeErr
		}
		wanted, readErr := s.Store.ReadManagedDesired(ctx, database.BindingID)
		if readErr == nil {
			managed.Intent = &plan.ManagedIntentFacts{Revision: wanted.Revision, EpochID: wanted.EpochID, RuntimeSHA256: wanted.RuntimeSHA256}
		} else if !errors.Is(readErr, store.ErrNotFound) {
			return apply.Facts{}, readErr
		}
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
			managed.OwnedNativeState = observeOwnedManagedState(ctx, s.managedObserver, *projection.Permit)
			if managed.Intent != nil {
				projectManagedIntent(managed.Intent, wanted, runtime)
			}
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
	routing, err := managedRoutingFacts(ctx, state, pol, snapshot, s.managedAuthority, s.Store)
	if err != nil {
		return apply.Facts{}, err
	}
	return apply.Facts{Input: plan.Input{Desired: desired, Snapshot: snapshot, State: state, Image: primary, Managed: managed}, Routing: routing}, nil
}

type managedRouteReader interface {
	ReadManagedRouteState(context.Context, string) (store.ManagedRouteState, error)
	NativeRouteWithdrawal(context.Context, store.ManagedRouteState, string, uint64) (store.NativePreparedRoutes, error)
}

func managedRoutingFacts(ctx context.Context, state plan.BrineState, pol policy.Policy, snapshot target.Snapshot, authority caddy.ManagedIngressAuthority, routes managedRouteReader) (caddy.State, error) {
	routing := caddy.State{Files: map[string]string{}, Sites: map[string]caddy.Site{}}
	heads := []store.ManagedRouteState{}
	for _, release := range state.Releases {
		var site caddy.Site
		var err error
		if release.Managed != nil {
			if routes == nil {
				return caddy.State{}, result.New(result.Conflict, nil)
			}
			current, readErr := routes.ReadManagedRouteState(ctx, release.App)
			if readErr != nil || current.BindingID != release.Managed.Database.BindingID || !current.Valid() {
				return caddy.State{}, errors.Join(result.New(result.Conflict, nil), readErr)
			}
			if !current.Present {
				continue
			}
			heads = append(heads, current)
			release.CaddyFile.Hash = current.FileHash
			site, err = caddy.CommittedManagedSite(release.Desired, *release.Managed)
			if err == nil {
				site, err = caddy.WithManagedAuthority(site, authority)
			}
		} else {
			port := release.HostPort
			if port > 65535 {
				return caddy.State{}, result.New(result.Conflict, nil)
			}
			site, err = caddy.NewSite(release.Desired.App(), pol, spec.Port(port))
		}
		if err != nil {
			return caddy.State{}, err
		}
		routing.Files[release.CaddyFile.Name] = release.CaddyFile.Hash
		routing.Sites[release.CaddyFile.Name] = site
	}
	if snapshot.CaddyConfig.Status == target.KnownStatus && snapshot.CaddyConfig.Value != nil {
		routing.Generation = snapshot.CaddyConfig.Value.Generation
		return managedWithdrawnRouting(ctx, routing, heads, *snapshot.CaddyConfig.Value, pol.Hash(), routes)
	} else if snapshot.CaddyConfig.Status != target.Absent {
		return caddy.State{}, result.New(result.Conflict, nil)
	}
	return routing, nil
}

func managedWithdrawnRouting(ctx context.Context, routing caddy.State, heads []store.ManagedRouteState, observed target.CaddyConfigSet, policyHash string, reader managedRouteReader) (caddy.State, error) {
	files := make(map[string]string, len(observed.Files))
	for _, file := range observed.Files {
		if _, duplicate := files[file.Name]; duplicate {
			return caddy.State{}, result.New(result.Conflict, nil)
		}
		files[file.Name] = file.Hash
	}
	for _, head := range heads {
		name := head.App + ".caddy"
		if _, present := files[name]; present {
			continue
		}
		withdrawal, err := reader.NativeRouteWithdrawal(ctx, head, policyHash, observed.Generation)
		if err != nil || !maps.Equal(withdrawal.PreviousFiles, routing.Files) || !maps.Equal(withdrawal.CurrentFiles, files) {
			return caddy.State{}, errors.Join(result.New(result.Conflict, nil), err)
		}
		delete(routing.Files, name)
		delete(routing.Sites, name)
	}
	return routing, nil
}

func projectManagedIntent(into *plan.ManagedIntentFacts, wanted store.ManagedDesiredState, runtime data.EngineRuntime) {
	fingerprint, err := managedRuntimeHash(runtime)
	if err != nil || !wanted.Current(runtime.BindingID, runtime.EpochID, fingerprint) {
		return
	}
	into.Current = true
	into.EngineRunning, into.FrontendRunning, into.IngressServing = wanted.EngineRunning, wanted.FrontendRunning, wanted.IngressServing
}

func observeOwnedManagedState(ctx context.Context, observer *managedEngineObserver, permit store.EnginePermit) target.Observation[string] {
	if observer == nil {
		return target.Observation[string]{Status: target.Unknown}
	}
	state, err := observer.ownedState(ctx, permit)
	if err != nil {
		return target.Observation[string]{Status: target.Unknown}
	}
	return target.Known(state)
}
