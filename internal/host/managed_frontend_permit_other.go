//go:build !linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/data"
)

type ManagedFrontendRuntime struct{}

func OpenManagedFrontendPermit(context.Context) (*ManagedFrontendRuntime, error) {
	return nil, data.ErrUnsupportedEngine
}
func (*ManagedFrontendRuntime) Permit(context.Context, string, string) error {
	return data.ErrUnsupportedEngine
}
func (*ManagedFrontendRuntime) Close() error { return nil }
