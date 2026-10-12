//go:build !linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/result"
)

type nativeMaterialStatusReader struct{ stateDir string }

func (nativeMaterialStatusReader) DeliverMaterial(context.Context, dispatch.NativeMaterialArgs) (nativematerial.Receipt, error) {
	return nativematerial.Receipt{}, result.New(result.DependencyMissing, nil)
}
func (nativeMaterialStatusReader) InspectMaterial(context.Context, dispatch.NativeMaterialInspectArgs) (nativematerial.Receipt, error) {
	return nativematerial.Receipt{}, result.New(result.DependencyMissing, nil)
}
