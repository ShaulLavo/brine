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
	return nil
}
