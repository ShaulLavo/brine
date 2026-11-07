package store

import (
	"context"
	"database/sql"

	"github.com/ShaulLavo/brine/internal/data"
)

// ReadinessBinding describes ownership only, not runtime or backup health.
// Exactly one concrete binding is populated, selected by Engine.
type ReadinessBinding struct {
	App         string
	Engine      data.EngineKind
	SQLite      data.SQLiteDatabaseBinding
	SpacetimeDB data.SpacetimeDBBinding
}

// ReadReadinessBindings includes reservations without a runtime or release.
// Missing engine or binding rows must not disappear through an inner join.
func (s *Store) ReadReadinessBindings(ctx context.Context, limit int) ([]ReadinessBinding, error) {
	if !s.readOnly || limit < 1 || limit > 16 {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	indexes, err := readReadinessBindingIndexes(ctx, tx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ReadinessBinding, 0, len(indexes))
	for _, index := range indexes {
		binding, err := s.readReadinessBinding(ctx, tx, index)
		if err != nil {
			return nil, err
		}
		out = append(out, binding)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

type readinessBindingIndex struct {
	app, owner string
	engine     data.EngineKind
	database   data.SpacetimeDBBinding
}

func readReadinessBindingIndexes(ctx context.Context, tx *sql.Tx, limit int) ([]readinessBindingIndex, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.app,COALESCE(i.app,''),a.incarnation_id,
		COALESCE(d.id,''),COALESCE(d.name,''),COALESCE(d.root,''),COALESCE(d.relative_directory,''),
		COALESCE(b.id,''),COALESCE(k.engine,'')
		FROM data_active_incarnations a
		LEFT JOIN data_incarnations i ON i.id=a.incarnation_id
		LEFT JOIN data_databases d ON d.incarnation_id=a.incarnation_id
		LEFT JOIN data_replica_bindings b ON b.database_id=d.id
		LEFT JOIN data_engine_kinds k ON k.database_id=d.id
		ORDER BY a.app,d.name LIMIT ?`, limit+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []readinessBindingIndex{}
	for rows.Next() {
		var index readinessBindingIndex
		d := &index.database
		if err := rows.Scan(&index.app, &index.owner, &d.IncarnationID, &d.DatabaseID, &d.Name, &d.Root, &d.RelativeDirectory, &d.BindingID, &index.engine); err != nil {
			return nil, err
		}
		if len(out) == limit {
			return nil, ErrInvalid
		}
		out = append(out, index)
	}
	return out, rows.Err()
}

func (s *Store) readReadinessBinding(ctx context.Context, tx *sql.Tx, index readinessBindingIndex) (ReadinessBinding, error) {
	out := ReadinessBinding{App: index.app, Engine: index.engine}
	if index.app == "" || index.app != index.owner || index.database.Validate() != nil {
		return out, &IntegrityError{}
	}
	switch index.engine {
	case data.EngineSQLite:
		permit, err := readReplicaPermit(ctx, tx, index.database.DatabaseID)
		if err != nil {
			return out, err
		}
		if err := s.validateReplicaPermit(ctx, tx, permit); err != nil {
			return out, err
		}
		d := permit.Database
		identity := data.SpacetimeDBBinding{DatabaseID: d.DatabaseID, Name: d.Name, IncarnationID: d.IncarnationID, Root: d.Root, RelativeDirectory: d.RelativeDirectory, BindingID: d.ReplicaBindingID}
		if identity != index.database {
			return out, &IntegrityError{}
		}
		out.SQLite = d
	case data.EngineSpacetimeDB:
		reserved, err := readReservedEngine(ctx, tx, index.database.BindingID)
		if err != nil {
			return out, err
		}
		if reserved.Database != index.database {
			return out, &IntegrityError{}
		}
		out.SpacetimeDB = reserved.Database
	default:
		return out, &IntegrityError{}
	}
	return out, nil
}
