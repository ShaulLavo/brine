package store

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

type sqliteRemovalSource struct {
	source   sqliteprep.StoppedSource
	hold     *SQLiteSourceHold
	baseline sqliteprep.StoppedBundle
}

type sqliteRemovalChild struct {
	guard  *SQLiteMutationGuard
	source atomic.Pointer[sqliteRemovalSource]
}

type sqliteRemovalAuthority struct {
	policy policy.Policy
	target string
}

// SQLiteRemovalGuard retains one host lock and exact per-database capabilities.
// Its concrete identity, not a successful interface callback, admits effects.
type SQLiteRemovalGuard struct {
	store     *Store
	host      Lock
	children  []*sqliteRemovalChild
	released  atomic.Bool
	authority atomic.Pointer[sqliteRemovalAuthority]
}

func (s *Store) AcquireSQLiteRemovalGuard(ctx context.Context, databases []data.DatabaseID) (*SQLiteRemovalGuard, error) {
	if s.readOnly || s.previewHost != nil || len(databases) == 0 || len(databases) > 16 {
		return nil, ErrInvalid
	}
	var previous data.DatabaseID
	for _, id := range databases {
		if !data.ValidID(string(id)) || previous != "" && previous >= id {
			return nil, ErrInvalid
		}
		previous = id
	}
	lock, err := s.AcquireHostLock(ctx)
	if err != nil {
		return nil, err
	}
	g := &SQLiteRemovalGuard{store: s, host: lock}
	if err = g.loadChildren(ctx, databases); err != nil {
		return nil, errors.Join(err, g.Close())
	}
	return g, nil
}

func (g *SQLiteRemovalGuard) loadChildren(ctx context.Context, databases []data.DatabaseID) error {
	for _, database := range databases {
		permit, err := readReplicaPermit(ctx, g.store.db, database)
		if err != nil {
			return err
		}
		child := &SQLiteMutationGuard{store: g.store, database: database, binding: permit.Replica.BindingID, removal: g}
		g.children = append(g.children, &sqliteRemovalChild{guard: child})
	}
	return nil
}

func (g *SQLiteRemovalGuard) live(s *Store) bool {
	return g != nil && g.store == s && s != nil && !g.released.Load()
}

func (g *SQLiteRemovalGuard) Child(database data.DatabaseID) (*SQLiteMutationGuard, error) {
	if g == nil || !g.live(g.store) {
		return nil, ErrConflict
	}
	for _, child := range g.children {
		if child.guard.database == database {
			return child.guard, nil
		}
	}
	return nil, ErrConflict
}

// BindAuthority is host composition only. Serialized task inputs never carry it.
func (g *SQLiteRemovalGuard) BindAuthority(pol policy.Policy, target string) error {
	if g == nil || !g.live(g.store) || !validSQLiteTaggedHash(target) {
		return ErrConflict
	}
	if _, ok := pol.SQLiteRemovalBounds(); !ok {
		return ErrInvalid
	}
	raw, err := pol.CanonicalBytes()
	if err != nil || "sha256:"+sqliteHash(raw) != pol.Hash() {
		return ErrInvalid
	}
	if !g.authority.CompareAndSwap(nil, &sqliteRemovalAuthority{policy: pol, target: target}) {
		return ErrConflict
	}
	return nil
}

func validSQLiteTaggedHash(hash string) bool {
	return len(hash) == 71 && hash[:7] == "sha256:" && data.ValidCatalogHash(hash[7:])
}

func (g *SQLiteRemovalGuard) AttachSource(ctx context.Context, database data.DatabaseID, source sqliteprep.StoppedSource) error {
	child, err := g.Child(database)
	if err != nil {
		return err
	}
	hold := sqliteHoldOf(source)
	if !hold.live(g.store, database) || hold.guard != child {
		return ErrConflict
	}
	baseline, err := source.Recheck(ctx)
	if err != nil || baseline.Validate() != nil || !hold.live(g.store, database) {
		return errors.Join(ErrConflict, err)
	}
	for _, entry := range g.children {
		if entry.guard == child && entry.source.CompareAndSwap(nil, &sqliteRemovalSource{source: source, hold: hold, baseline: baseline}) {
			return nil
		}
	}
	return ErrConflict
}

func (g *SQLiteRemovalGuard) Recheck(ctx context.Context) error {
	if g == nil || !g.live(g.store) {
		return ErrConflict
	}
	for _, child := range g.children {
		if err := g.recheckChild(ctx, child, nil); err != nil {
			return err
		}
	}
	return nil
}

func (g *SQLiteRemovalGuard) recheckChild(ctx context.Context, child *sqliteRemovalChild, expected *sqliteprep.StoppedBundle) error {
	source := child.source.Load()
	if source == nil || !source.hold.live(g.store, child.guard.database) || source.hold.guard != child.guard {
		return ErrConflict
	}
	observed, err := source.source.Recheck(ctx)
	if err != nil || !source.baseline.Matches(observed) || expected != nil && !expected.Matches(observed) || !source.hold.live(g.store, child.guard.database) || !g.live(g.store) {
		return errors.Join(sqliteRefuse(sqliteprep.StoppedSourceChanged), err)
	}
	return nil
}

func (g *SQLiteRemovalGuard) recheckPrepared(ctx context.Context, database data.DatabaseID, expected sqliteprep.StoppedBundle) error {
	if g == nil || !g.live(g.store) {
		return ErrConflict
	}
	for _, child := range g.children {
		if child.guard.database == database {
			return g.recheckChild(ctx, child, &expected)
		}
	}
	return ErrConflict
}

func (g *SQLiteRemovalGuard) Close() error {
	if g == nil || g.released.Swap(true) {
		return nil
	}
	var err error
	for i := len(g.children) - 1; i >= 0; i-- {
		err = errors.Join(err, closeSQLiteRemovalChild(g.children[i]))
	}
	return errors.Join(err, g.host.Release())
}

func closeSQLiteRemovalChild(child *sqliteRemovalChild) error {
	var err error
	if source := child.source.Load(); source != nil {
		err = source.source.Close()
	}
	if hold := child.guard.source.Load(); hold != nil {
		err = errors.Join(err, hold.Release())
	}
	return errors.Join(err, child.guard.Release())
}

func (g *SQLiteRemovalGuard) recheckEmpty(ctx context.Context, d archive.SQLiteDatabase) error {
	if err := g.Recheck(ctx); err != nil {
		return err
	}
	for _, child := range g.children {
		if child.guard.database != d.Binding.DatabaseID {
			continue
		}
		source := child.source.Load()
		if source == nil || d.Allocation == nil {
			return ErrConflict
		}
		b := source.baseline
		if b.DB.Presence != sqliteprep.Absent || b.WAL.Presence != sqliteprep.Absent || b.SHM.Presence != sqliteprep.Absent || b.Journal.Presence != sqliteprep.Absent || b.Location.Device != d.Allocation.DirectoryDevice || b.Location.Inode != d.Allocation.DirectoryInode {
			return ErrConflict
		}
		return nil
	}
	return ErrConflict
}

func (g *SQLiteRemovalGuard) sourcesLive() bool {
	if g == nil || !g.live(g.store) {
		return false
	}
	for _, child := range g.children {
		source := child.source.Load()
		if source == nil || source.hold.guard != child.guard || !source.hold.live(g.store, child.guard.database) {
			return false
		}
	}
	return true
}
