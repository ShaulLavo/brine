//go:build !linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/data"
)

func (managedEngineObserver) sourceAbsent(context.Context, data.SpacetimeDBBinding) error {
	return data.ErrUnsupportedEngine
}
func (managedEngineObserver) current(context.Context, data.ReplicaBindingID, string) (managedCurrentProof, error) {
	return managedCurrentProof{}, data.ErrUnsupportedEngine
}
