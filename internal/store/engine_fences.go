package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
)

func (s *Store) guardEngineDatabase(ctx context.Context, database data.DatabaseID) (ops.Lock, error) {
	var kind data.EngineKind
	var binding data.ReplicaBindingID
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(k.engine,''),b.id FROM data_databases d LEFT JOIN data_engine_kinds k ON k.database_id=d.id JOIN data_replica_bindings b ON b.database_id=d.id WHERE d.id=?", database).Scan(&kind, &binding)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	switch kind {
	case data.EngineSQLite:
		return previewLock{}, nil
	case data.EngineSpacetimeDB:
		return data.AcquireEngineLaunchGuard(ctx, s.dir, binding)
	default:
		return nil, &IntegrityError{}
	}
}

func (s *Store) guardEngineFence(ctx context.Context, fence data.FenceID) (ops.Lock, error) {
	var database data.DatabaseID
	err := s.db.QueryRowContext(ctx, "SELECT database_id FROM data_fences WHERE id=?", fence).Scan(&database)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return s.guardEngineDatabase(ctx, database)
}

type engineGuards []ops.Lock

func (locks engineGuards) Release() error {
	var err error
	for i := len(locks) - 1; i >= 0; i-- {
		err = errors.Join(err, locks[i].Release())
	}
	return err
}

func (s *Store) guardEngineDatabases(ctx context.Context, databases []data.DatabaseID) (engineGuards, error) {
	databases = slices.Clone(databases)
	slices.Sort(databases)
	databases = slices.Compact(databases)
	locks := engineGuards{}
	for _, database := range databases {
		lock, err := s.guardEngineDatabase(ctx, database)
		if err != nil {
			return nil, errors.Join(err, locks.Release())
		}
		locks = append(locks, lock)
	}
	return locks, nil
}

func readFenceIdentity(ctx context.Context, tx *sql.Tx, id data.DatabaseID) (data.AppIncarnationID, error) {
	var kind data.EngineKind
	var incarnation data.AppIncarnationID
	err := tx.QueryRowContext(ctx, "SELECT COALESCE(k.engine,''),d.incarnation_id FROM data_databases d LEFT JOIN data_engine_kinds k ON k.database_id=d.id WHERE d.id=?", id).Scan(&kind, &incarnation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !kind.Valid() || !data.ValidID(string(incarnation)) {
		return "", &IntegrityError{}
	}
	if kind == data.EngineSQLite {
		permit, err := readReplicaPermit(ctx, tx, id)
		return permit.Database.IncarnationID, err
	}
	var binding data.ReplicaBindingID
	if err = tx.QueryRowContext(ctx, "SELECT id FROM data_replica_bindings WHERE database_id=?", id).Scan(&binding); err != nil {
		return "", err
	}
	reserved, err := readReservedEngine(ctx, tx, binding)
	if err != nil || reserved.Database.DatabaseID != id || reserved.Database.IncarnationID != incarnation {
		return "", errors.Join(&IntegrityError{}, err)
	}
	return incarnation, nil
}
