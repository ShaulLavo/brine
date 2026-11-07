package host

import (
	"errors"
	"sync"

	"github.com/ShaulLavo/brine/internal/store"
)

type stateMode uint8

const (
	stateMutating stateMode = iota
	stateReadOnly
	stateSnapshot
	statePreview
)

// gatedStore owns both SQLite and its admission lease. The lease is released
// only after SQLite and any isolated snapshot have closed.
type gatedStore struct {
	*store.Store
	lease interface{ Close() error }
	once  sync.Once
	err   error
}

func (s *gatedStore) Close() error {
	s.once.Do(func() { s.err = errors.Join(s.Store.Close(), s.lease.Close()) })
	return s.err
}
