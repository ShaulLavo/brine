package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

// SQLiteTask derives a SQLite preparation task and its canonical input from
// the real operation. A request packet never supplies either.
func (s *Store) SQLiteTask(ctx context.Context, operationID string) (sqliteprep.Task, sqliteprep.Input, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return sqliteprep.Task{}, sqliteprep.Input{}, err
	}
	defer func() { _ = tx.Rollback() }()
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", operationID))
	if err != nil {
		return sqliteprep.Task{}, sqliteprep.Input{}, err
	}
	task, in, err := readSQLiteTask(ctx, tx, op)
	if err != nil {
		return sqliteprep.Task{}, sqliteprep.Input{}, err
	}
	return task, in, tx.Commit()
}

// SQLitePreparationOwner is the protected app, database and requester of a
// recorded preparation. Admission uses it to select a task input; it grants
// nothing, and the task revalidates every join under the host mutation lock.
type SQLitePreparationOwner struct {
	App       string
	Database  data.DatabaseID
	Requester string
}

func (s *Store) SQLitePreparationOwner(ctx context.Context, id string) (SQLitePreparationOwner, error) {
	if !data.ValidID(id) {
		return SQLitePreparationOwner{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SQLitePreparationOwner{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := readSQLitePreparation(ctx, tx, id)
	if err != nil {
		return SQLitePreparationOwner{}, err
	}
	owner := SQLitePreparationOwner{App: p.intent.Task.App, Database: p.intent.Scope.DatabaseID, Requester: p.intent.Task.Requester}
	return owner, tx.Commit()
}

// SQLitePreparedSource is the retained exact source of one completed
// preparation: its original scope, the origin restore point (for a reverify
// successor, the origin's point and key), its coverage cutoff, the registered
// definition of the recorded schema and the exact delivered credential
// receipt. Reading it changes nothing and authorizes no consumer.
type SQLitePreparedSource struct {
	PreparationID string
	Scope         sqliteprep.Scope
	Point         data.SQLiteRestorePoint
	Cutoff        time.Time
	Definitions   []data.SQLiteSchemaDefinition
	Credential    CredentialRecord
	Replica       data.ReplicaBinding
}

// ReadSQLitePreparedSource works through OpenReadOnly. ErrNotFound means the
// ID is not a SQLite preparation, so the caller may use its ordinary point
// path. Any other failure, including a preparation of another app or
// database, refuses rather than falling back.
func (s *Store) ReadSQLitePreparedSource(ctx context.Context, app, database, id string) (SQLitePreparedSource, error) {
	if !data.ValidID(id) {
		return SQLitePreparedSource{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SQLitePreparedSource{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_sqlite_preparations WHERE id=?", id).Scan(&exists); err != nil {
		return SQLitePreparedSource{}, err
	}
	if exists == 0 {
		return SQLitePreparedSource{}, ErrNotFound
	}
	// A recorded preparation never falls back: inner not-found errors are
	// not propagated as "not a preparation".
	out, err := readSQLitePreparedSource(ctx, tx, app, database, id)
	if err != nil {
		return SQLitePreparedSource{}, ErrConflict
	}
	return out, tx.Commit()
}

func readSQLitePreparedSource(ctx context.Context, tx *sql.Tx, app, database, id string) (SQLitePreparedSource, error) {
	p, err := readSQLitePreparation(ctx, tx, id)
	if err != nil {
		return SQLitePreparedSource{}, err
	}
	scope := p.intent.Scope
	if p.intent.Task.App != app || database != "" && string(scope.DatabaseName) != database || p.point == nil || p.state != ops.Succeeded {
		return SQLitePreparedSource{}, ErrConflict
	}
	op, _, err := readSQLiteOperation(ctx, tx, p.intent.Task)
	if err != nil {
		return SQLitePreparedSource{}, err
	}
	if _, err = readSQLiteSuccess(ctx, tx, op, p.intent.Task); err != nil {
		return SQLitePreparedSource{}, err
	}
	out := SQLitePreparedSource{PreparationID: id, Scope: scope, Point: p.point.Source.Point, Cutoff: p.point.Source.Cutoff}
	if out.Replica, err = readSQLiteRetainedReplica(ctx, tx, scope); err != nil {
		return SQLitePreparedSource{}, err
	}
	if out.Definitions, err = readSQLiteRetainedDefinition(ctx, tx, scope); err != nil {
		return SQLitePreparedSource{}, err
	}
	out.Credential, err = readSQLiteCredential(ctx, tx, app, out.Replica, scope.CredentialVersion)
	return out, err
}

// readSQLiteRetainedReplica requires the active incarnation's current binding
// to be the retained source's binding, epoch, destination and prefix.
func readSQLiteRetainedReplica(ctx context.Context, tx *sql.Tx, scope sqliteprep.Scope) (data.ReplicaBinding, error) {
	if err := checkSQLiteTaskDatabase(ctx, tx, scope.App, scope.DatabaseID); err != nil {
		if retiredErr := readSQLiteRetiredScope(ctx, tx, scope); retiredErr != nil {
			return data.ReplicaBinding{}, errors.Join(ErrConflict, err, retiredErr)
		}
	}
	permit, err := readReplicaPermit(ctx, tx, scope.DatabaseID)
	if err != nil {
		return data.ReplicaBinding{}, err
	}
	r := permit.Replica
	destination, err := json.Marshal(r.Destination)
	if err != nil || permit.Database.IncarnationID != scope.IncarnationID || permit.Database.Name != scope.DatabaseName || r.BindingID != scope.BindingID || r.EpochID != scope.EpochID || sqliteHash(destination) != scope.TargetSHA256 || strings.TrimSuffix(r.RemotePrefix, "/")+"/" != scope.RemotePrefix {
		return data.ReplicaBinding{}, ErrConflict
	}
	return r, nil
}

// readSQLiteRetainedDefinition returns the immutable registry entry of the
// recorded schema. The empty marker needs no definition.
func readSQLiteRetainedDefinition(ctx context.Context, tx *sql.Tx, scope sqliteprep.Scope) ([]data.SQLiteSchemaDefinition, error) {
	schema := scope.Schema
	if schema.Marker == data.EmptyMarker {
		return []data.SQLiteSchemaDefinition{}, nil
	}
	var catalog string
	err := tx.QueryRowContext(ctx, "SELECT catalog_sha256 FROM data_schema_definitions WHERE database_id=? AND marker=?", scope.DatabaseID, schema.Marker).Scan(&catalog)
	if err != nil || catalog != schema.CatalogSHA256 {
		return nil, errors.Join(ErrConflict, err)
	}
	return []data.SQLiteSchemaDefinition{{Database: scope.DatabaseName, Marker: schema.Marker, CatalogSHA256: catalog}}, nil
}
