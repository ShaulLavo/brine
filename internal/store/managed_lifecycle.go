package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type ManagedAppProjection struct {
	App       string
	Reserved  ReservedEngine
	Permit    *EnginePermit
	FenceHeld bool
}

// ReadManagedApp resolves one current binding from protected app ownership.
// Runtime/permit absence remains explicit; a reservation cannot start a server.
func (s *Store) ReadManagedApp(ctx context.Context, app string) (ManagedAppProjection, error) {
	if !validAppName(app) {
		return ManagedAppProjection{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ManagedAppProjection{}, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT b.id FROM data_active_incarnations a
		JOIN data_databases d ON d.incarnation_id=a.incarnation_id
		JOIN data_engine_kinds k ON k.database_id=d.id AND k.engine='spacetimedb'
		JOIN data_replica_bindings b ON b.database_id=d.id WHERE a.app=? ORDER BY b.id`, app)
	if err != nil {
		return ManagedAppProjection{}, err
	}
	var bindings []data.ReplicaBindingID
	for rows.Next() {
		var binding data.ReplicaBindingID
		if err := rows.Scan(&binding); err != nil {
			_ = rows.Close()
			return ManagedAppProjection{}, err
		}
		bindings = append(bindings, binding)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return ManagedAppProjection{}, err
	}
	if len(bindings) == 0 {
		return ManagedAppProjection{}, ErrNotFound
	}
	if len(bindings) != 1 || !data.ValidID(string(bindings[0])) {
		return ManagedAppProjection{}, &IntegrityError{}
	}
	reserved, err := readReservedEngine(ctx, tx, bindings[0])
	if err != nil {
		return ManagedAppProjection{}, err
	}
	projection := ManagedAppProjection{App: app, Reserved: reserved}
	var held int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM data_fences WHERE database_id=? AND state='held'", reserved.Database.DatabaseID).Scan(&held); err != nil {
		return ManagedAppProjection{}, err
	}
	projection.FenceHeld = held != 0
	var runtimes int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM data_engine_runtime_commits WHERE binding_id=?", bindings[0]).Scan(&runtimes); err != nil {
		return ManagedAppProjection{}, err
	}
	if runtimes != 0 {
		permit, err := readEnginePermit(ctx, tx, bindings[0])
		if err != nil {
			return ManagedAppProjection{}, err
		}
		projection.Permit = &permit
	}
	return projection, tx.Commit()
}

func (p ManagedAppProjection) Unpublished() bool {
	return p.Permit == nil || p.Permit.Runtime.State == data.EngineUnpublished
}

func (p ManagedAppProjection) AllowsInspection(now time.Time) bool {
	return p.Permit != nil && p.Permit.AllowsVerification(now)
}

func validAppName(app string) bool {
	if len(app) == 0 || len(app) > 63 || app[0] < 'a' || app[0] > 'z' {
		return false
	}
	for _, character := range app {
		if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return app[len(app)-1] != '-'
}
