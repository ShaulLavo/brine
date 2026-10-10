//go:build !linux

package replication

import "context"

type LifetimeLock struct{}

func (*LifetimeLock) Release() error                                     { return ErrInvalid }
func (*LifetimeLock) inherit() error                                     { return ErrInvalid }
func AcquireLifetimeLock(context.Context, string) (*LifetimeLock, error) { return nil, ErrInvalid }
