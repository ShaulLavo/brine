//go:build !linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
)

func (managedEngineObserver) sourceAbsent(context.Context, data.SpacetimeDBBinding) error {
	return data.ErrUnsupportedEngine
}
func (managedEngineObserver) current(context.Context, data.ReplicaBindingID, string) (managedCurrentProof, error) {
	return managedCurrentProof{}, data.ErrUnsupportedEngine
}

func (managedEngineObserver) stopped(context.Context, store.EnginePermit) error {
	return data.ErrUnsupportedEngine
}
