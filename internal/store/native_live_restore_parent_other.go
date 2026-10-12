//go:build !linux

package store

import (
	"context"
	"os"
)

func checkNativeRestoreAnchors(context.Context, *NativeLiveRestoreExecution, *os.Root, NativeRestoreObservation) error {
	return ErrInvalid
}
