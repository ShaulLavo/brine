//go:build !linux

package store

import (
	"context"
	"errors"
	"os"
)

type Lock interface{ Release() error }

var errHostOnly = errors.New("control store requires a Linux host")

func openPrivateFile(string) (*os.File, error)                 { return nil, errHostOnly }
func (s *Store) AcquireHostLock(context.Context) (Lock, error) { return nil, errHostOnly }
