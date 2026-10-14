//go:build linux

package host

import (
	"context"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/data"
)

func readPublicationUnit(ctx context.Context, home string, runtime data.EngineRuntime, initial bool) (string, error) {
	if !initial {
		return readNativeRestoreUnit(ctx, home, runtime)
	}
	units, err := openManagedUnits(home)
	if err != nil {
		return "", err
	}
	defer func() { _ = units.Close() }()
	_, present, err := units.read(ctx, filepath.Join(managedServiceDirectory, "brine-engine-"+string(runtime.BindingID)+".service"))
	expected, renderErr := EngineServiceBytes(runtime)
	if err != nil || renderErr != nil || present || managedServiceHash(expected) != runtime.UnitSHA256 {
		return "", errEngineSource
	}
	return "", nil
}
