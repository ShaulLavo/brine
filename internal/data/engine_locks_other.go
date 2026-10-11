//go:build !linux

package data

import (
	"context"
	"errors"
	"os"
)

var ErrEngineLocked = errors.New("engine lock is held")

func engineLockCurrent(*os.File, string) bool { return false }

func PrepareEngineLocks(context.Context, string, ReplicaBindingID) error {
	return ErrUnsupportedEngine
}
func AcquireEngineLaunchGuard(context.Context, string, ReplicaBindingID) (*EngineLock, error) {
	return nil, ErrUnsupportedEngine
}
func AcquireEngineLifetimeLock(context.Context, string, ReplicaBindingID) (*EngineLock, error) {
	return nil, ErrUnsupportedEngine
}
