//go:build !linux

package store

import (
	"context"
	"github.com/ShaulLavo/brine/internal/data"
)

func checkNativeRestoreTargetAnchors(context.Context, *NativeLiveRestoreExecution, nativeLiveRestoreVerifiedRecord, NativeRestoreTargetObservation) error {
	return ErrConflict
}

func checkNativeRestoreTransitionAnchors(context.Context, *NativeRestoreTransition, data.EngineTransition, NativeRestoreTransitionObservation) error {
	return ErrConflict
}
