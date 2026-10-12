//go:build linux

package host

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type managedStatusReader struct {
	state       *store.Store
	preparation DataPreparation
	observer    managedEngineObserver
}

func (r managedStatusReader) Read(ctx context.Context, app string) (apps.ManagedStatus, error) {
	if r.state == nil {
		return apps.ManagedStatus{}, store.ErrInvalid
	}
	projection, err := r.state.ReadManagedApp(ctx, app)
	if err != nil {
		return apps.ManagedStatus{}, err
	}
	database, archive := projection.Reserved.Database, projection.Reserved.Archive
	status := apps.ManagedStatus{BindingID: database.BindingID, EpochID: archive.EpochID, State: "reserved", Fenced: projection.FenceHeld, EngineUnit: "brine-engine-" + string(database.BindingID) + ".service", EnginePort: target.Observation[target.Port]{Status: target.Unknown}, EngineActive: target.Observation[bool]{Status: target.Unknown}, EngineLogs: "unavailable"}
	prepared, err := r.state.ReadManagedPreparation(ctx, database.BindingID)
	if err == nil {
		status.EnginePort = target.Known(target.Port(prepared.EnginePort))
		status.Frontend = prepared.FrontendPort != 0
		if status.Frontend {
			port := target.Port(prepared.FrontendPort)
			status.FrontendPort = &port
		}
		planned, desired, err := r.state.LoadPlan(ctx, prepared.PlanID)
		if err != nil {
			return status, err
		}
		if projection.Permit == nil {
			ready, err := r.preparation.ManagedPrepared(ctx, prepared.OperationID, planned, desired)
			if err == nil && ready {
				status.Prepared = true
				status.State = "unpublished"
				status.EngineActive = target.Known(false)
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return status, err
	}
	if projection.Permit != nil {
		runtime := projection.Permit.Runtime
		status.State = string(runtime.State)
		status.EnginePort = target.Known(target.Port(runtime.HostPort))
		status.Frontend = false
		status.FrontendPort = nil
		current, releaseErr := r.state.CurrentRelease(ctx, app)
		if releaseErr == nil {
			if current.Managed == nil || current.Managed.BindingID != database.BindingID {
				return status, store.ErrInvalid
			}
			if current.Managed.Frontend != nil {
				status.Frontend = true
				port := current.Managed.Frontend.Port
				status.FrontendPort = &port
			}
		} else if !errors.Is(releaseErr, store.ErrNotFound) {
			return status, releaseErr
		}
		if runtime.State == data.EngineInitialized {
			fingerprint, err := managedRuntimeHash(runtime)
			if err != nil {
				return status, err
			}
			proof, err := r.observer.current(ctx, database.BindingID, fingerprint)
			if err == nil && !proof.observedAt.IsZero() {
				status.EngineActive = target.Known(true)
				status.Prepared = true
			}
		}
	}
	return status, nil
}
