package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

const sqliteTaskInputLimit = 4096

// SaveSQLiteTaskInput records the canonical reference-only input of a SQLite
// preparation task before its operation exists. Its ID is the canonical SHA-256
// the operation references. It selects a database; it authorizes nothing.
func (s *Store) SaveSQLiteTaskInput(ctx context.Context, in sqliteprep.Input) (string, error) {
	if s.readOnly || in.Validate() != nil {
		return "", ErrInvalid
	}
	raw, err := sqliteprep.Encode(in)
	if err != nil || len(raw) > sqliteTaskInputLimit {
		return "", ErrInvalid
	}
	id, err := sqliteprep.Fingerprint(in)
	if err != nil {
		return "", ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if err = checkSQLiteTaskDatabase(ctx, tx, in.App, in.DatabaseID); err != nil {
		return "", err
	}
	var supersedes any
	if in.Supersedes != "" {
		supersedes = in.Supersedes
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_task_inputs VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING", id, in.Kind, in.App, in.DatabaseID, in.PreparationID, supersedes, raw); err != nil {
		return "", err
	}
	var stored []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_sqlite_task_inputs WHERE id=?", id).Scan(&stored); err != nil {
		return "", err
	}
	if !bytes.Equal(stored, raw) {
		return "", ErrConflict
	}
	return id, tx.Commit()
}

// checkSQLiteTaskDatabase requires a SQLite database of the app's active incarnation.
func checkSQLiteTaskDatabase(ctx context.Context, q dataQuerier, app string, database data.DatabaseID) error {
	var engine string
	err := q.QueryRowContext(ctx, "SELECT COALESCE(k.engine,'') FROM data_databases d JOIN data_active_incarnations a ON a.incarnation_id=d.incarnation_id LEFT JOIN data_engine_kinds k ON k.database_id=d.id WHERE d.id=? AND a.app=?", database, app).Scan(&engine)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if engine != string(data.EngineSQLite) {
		return ErrConflict
	}
	return nil
}

func isSQLiteTaskKind(kind ops.Kind) bool {
	return kind == ops.SQLitePrepare || kind == ops.SQLitePrepareAbandon
}

// readSQLiteTask joins a real operation with its canonical input record. The
// task identity is derived from the operation, never from a request packet.
func readSQLiteTask(ctx context.Context, q dataQuerier, op ops.Operation) (sqliteprep.Task, sqliteprep.Input, error) {
	if !isSQLiteTaskKind(op.Kind) {
		return sqliteprep.Task{}, sqliteprep.Input{}, ErrConflict
	}
	var kind, app, preparation string
	var database data.DatabaseID
	var supersedes sql.NullString
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT kind,app,database_id,preparation_id,supersedes,canonical FROM data_sqlite_task_inputs WHERE id=?", op.SecretRef).Scan(&kind, &app, &database, &preparation, &supersedes, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return sqliteprep.Task{}, sqliteprep.Input{}, ErrNotFound
	}
	if err != nil {
		return sqliteprep.Task{}, sqliteprep.Input{}, err
	}
	in, err := sqliteprep.DecodeInput(raw)
	if err != nil {
		return sqliteprep.Task{}, sqliteprep.Input{}, &IntegrityError{}
	}
	if string(in.Kind) != kind || in.App != app || in.DatabaseID != database || in.PreparationID != preparation || in.Supersedes != supersedes.String || supersedes.Valid != (in.Supersedes != "") {
		return sqliteprep.Task{}, sqliteprep.Input{}, &IntegrityError{}
	}
	task := sqliteprep.Task{OperationID: op.ID, Kind: sqliteprep.TaskKind(op.Kind), Requester: op.Requester, App: op.App, InputSHA256: op.SecretRef}
	if !in.Matches(task) {
		return sqliteprep.Task{}, sqliteprep.Input{}, ErrConflict
	}
	return task, in, nil
}

// readSQLiteOperation reads the exact task operation and its input in tx.
func readSQLiteOperation(ctx context.Context, q dataQuerier, task sqliteprep.Task) (ops.Operation, sqliteprep.Input, error) {
	op, err := scanOperation(q.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", task.OperationID))
	if err != nil {
		return ops.Operation{}, sqliteprep.Input{}, err
	}
	joined, in, err := readSQLiteTask(ctx, q, op)
	if err != nil {
		return ops.Operation{}, sqliteprep.Input{}, err
	}
	if joined != task {
		return ops.Operation{}, sqliteprep.Input{}, ErrConflict
	}
	return op, in, nil
}
