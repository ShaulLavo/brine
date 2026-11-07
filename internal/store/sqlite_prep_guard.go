package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/replication"
)

// SQLiteMutationGuard is minted only by AcquireSQLiteMutationGuard after this
// store takes the real host mutation lock. Host adapters expose it through
// SQLiteMutationGuard(); a structurally similar port implementation without
// it cannot authorize a SQLite preparation journal effect.
type SQLiteMutationGuard struct {
	removal  *SQLiteRemovalGuard
	store    *Store
	host     Lock
	database data.DatabaseID
	binding  data.ReplicaBindingID
	released atomic.Bool
	source   atomic.Pointer[SQLiteSourceHold]
}

// SQLiteSourceHold retains the replica lifetime lock beneath a live mutation
// guard. The store holds it through final completion; it is not a file proof.
type SQLiteSourceHold struct {
	guard    *SQLiteMutationGuard
	lifetime *replication.LifetimeLock
	released atomic.Bool
}

type sqliteGuardBearer interface {
	SQLiteMutationGuard() *SQLiteMutationGuard
}
type sqliteHoldBearer interface {
	SQLiteSourceHold() *SQLiteSourceHold
}

// AcquireSQLiteMutationGuard takes the existing host mutation lock for one
// SQLite database. A read-only or preview store cannot mint the capability.
func (s *Store) AcquireSQLiteMutationGuard(ctx context.Context, database data.DatabaseID) (*SQLiteMutationGuard, error) {
	if s.readOnly || s.previewHost != nil || !data.ValidID(string(database)) {
		return nil, ErrInvalid
	}
	permit, err := readReplicaPermit(ctx, s.db, database)
	if err != nil {
		return nil, err
	}
	lock, err := s.AcquireHostLock(ctx)
	if err != nil {
		return nil, err
	}
	return &SQLiteMutationGuard{store: s, host: lock, database: database, binding: permit.Replica.BindingID}, nil
}

func (g *SQLiteMutationGuard) Release() error {
	if g == nil {
		return nil
	}
	if g.removal != nil {
		g.removal.commitMu.Lock()
		defer g.removal.commitMu.Unlock()
	}
	if g.released.Swap(true) {
		return nil
	}
	if g.removal != nil {
		return nil
	}
	return g.host.Release()
}

func (g *SQLiteMutationGuard) live(s *Store, database data.DatabaseID) bool {
	return g != nil && g.store == s && s != nil && g.database == database && !g.released.Load() && (g.removal == nil || g.removal.live(s))
}

// HoldSource acquires the replica lifetime lock without waiting. A running
// replica supervisor or any foreign holder refuses rather than blocks.
func (g *SQLiteMutationGuard) HoldSource(ctx context.Context) (*SQLiteSourceHold, error) {
	if g != nil && g.removal != nil {
		g.removal.commitMu.Lock()
		defer g.removal.commitMu.Unlock()
	}
	if g == nil || !g.live(g.store, g.database) {
		return nil, ErrConflict
	}
	lifetime, err := replication.AcquireLifetimeLock(ctx, filepath.Join(g.store.dir, "replica-locks", string(g.binding)+".lock"))
	if err != nil {
		return nil, errors.Join(ErrConflict, err)
	}
	hold := &SQLiteSourceHold{guard: g, lifetime: lifetime}
	if !g.source.CompareAndSwap(nil, hold) {
		_ = lifetime.Release()
		return nil, ErrConflict
	}
	return hold, nil
}

func (h *SQLiteSourceHold) Release() error {
	return h.ReleaseSource(nil)
}

// ReleaseSource closes the source's owned handles before releasing its lifetime
// lock. Removal commits exclude both actions; cleanup must not reenter the hold.
func (h *SQLiteSourceHold) ReleaseSource(cleanup func() error) error {
	if h == nil {
		if cleanup != nil {
			return cleanup()
		}
		return nil
	}
	if h.guard.removal != nil {
		h.guard.removal.commitMu.Lock()
		defer h.guard.removal.commitMu.Unlock()
	}
	var err error
	if cleanup != nil {
		err = cleanup()
	}
	if h.released.Swap(true) {
		return err
	}
	return errors.Join(err, h.lifetime.Release())
}

func (h *SQLiteSourceHold) live(s *Store, database data.DatabaseID) bool {
	return h != nil && !h.released.Load() && h.guard.live(s, database) && h.guard.source.Load() == h
}

// sqliteGuardOf accepts only the store-minted guard carried by a port.
func sqliteGuardOf(port any) *SQLiteMutationGuard {
	bearer, ok := port.(sqliteGuardBearer)
	if !ok {
		return nil
	}
	return bearer.SQLiteMutationGuard()
}

func sqliteHoldOf(port any) *SQLiteSourceHold {
	bearer, ok := port.(sqliteHoldBearer)
	if !ok {
		return nil
	}
	return bearer.SQLiteSourceHold()
}
