//go:build !linux

package store

import (
	"context"
	"errors"
	"os"

	"github.com/ShaulLavo/brine/internal/ops"
)

type Lock = ops.Lock

var errHostOnly = errors.New("control store requires a Linux host")

func openPrivateFile(string) (*os.File, error)                 { return nil, errHostOnly }
func (s *Store) AcquireHostLock(context.Context) (Lock, error) { return nil, errHostOnly }

func (s *Store) AcquireLaunchLock(context.Context) (Lock, error) { return nil, errHostOnly }

func secureStateDir(string) error { return errHostOnly }

func readOnlyOwner(string) error { return errHostOnly }
