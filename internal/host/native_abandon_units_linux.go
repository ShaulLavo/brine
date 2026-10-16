//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type nativeAbandonUnitStopper interface {
	Stop(context.Context, systemd.Unit) error
}

func stopNativeAbandonUnits(ctx context.Context, units nativeAbandonUnitStopper, expected store.NativeAbandonExpected) error {
	if expected.Release.Managed.Frontend != nil {
		frontend, err := systemd.ParseUnit(expected.App + ".service")
		if err != nil {
			return err
		}
		if err := units.Stop(ctx, frontend); err != nil {
			return err
		}
	}
	engine, err := systemd.ParseUnit(engineContainerName(expected.Permit.Runtime.BindingID) + ".service")
	if err != nil {
		return err
	}
	return units.Stop(ctx, engine)
}
