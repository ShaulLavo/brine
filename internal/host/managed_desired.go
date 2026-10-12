package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
)

func currentManagedDesired(ctx context.Context, state *store.Store, runtime data.EngineRuntime) (store.ManagedDesiredState, error) {
	if state == nil {
		return store.ManagedDesiredState{}, errEngineSource
	}
	fingerprint, err := managedRuntimeHash(runtime)
	if err != nil {
		return store.ManagedDesiredState{}, errEngineSource
	}
	wanted, err := state.ReadManagedDesired(ctx, runtime.BindingID)
	if err != nil || !wanted.Current(runtime.BindingID, runtime.EpochID, fingerprint) {
		return store.ManagedDesiredState{}, errEngineSource
	}
	return wanted, nil
}
