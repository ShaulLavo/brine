package store

import (
	"bytes"
	"context"
	"database/sql"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
)

// A definite launcher failure precedes the handler and has no task receipt.
// Its exact immutable launch journal is readable without granting completion.
func checkSQLiteRetireLaunchFailure(ctx context.Context, tx *sql.Tx, op ops.Operation) error {
	if op.State != ops.Failed {
		return &IntegrityError{}
	}
	var effects int
	err := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM data_sqlite_archive_attempts WHERE task_operation_id=?) +
 (SELECT count(*) FROM data_sqlite_archive_completions WHERE completing_operation_id=?) +
 (SELECT count(*) FROM data_sqlite_retire_settlements WHERE operation_id=?)`, op.ID, op.ID, op.ID).Scan(&effects)
	if err != nil {
		return err
	}
	if effects != 0 {
		return &IntegrityError{}
	}
	return checkSQLiteRetireLaunchEvents(ctx, tx, op)
}

func checkSQLiteRetireLaunchEvents(ctx context.Context, tx *sql.Tx, op ops.Operation) error {
	expected := []ops.Event{
		{Sequence: 1, Kind: "launch", Payload: []byte(`{"outcome":"intent"}`)},
		{Sequence: 2, Kind: "failure", Payload: []byte(`{"code":"launch_failed"}`)},
		{Sequence: 3, Kind: "state", State: ops.Failed},
	}
	rows, err := tx.QueryContext(ctx, "SELECT seq,kind,state,payload,created_at FROM events WHERE operation_id=? ORDER BY seq LIMIT 4", op.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		var event ops.Event
		var created string
		if err = rows.Scan(&event.Sequence, &event.Kind, &event.State, &event.Payload, &created); err != nil {
			return err
		}
		event.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil || count >= len(expected) || !sameSQLiteRetireLaunchEvent(op, event, expected[count]) {
			return &IntegrityError{}
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count != len(expected) {
		return &IntegrityError{}
	}
	return nil
}

func sameSQLiteRetireLaunchEvent(op ops.Operation, got, want ops.Event) bool {
	return got.Sequence == want.Sequence && got.Kind == want.Kind && got.State == want.State && bytes.Equal(got.Payload, want.Payload) && ops.ValidateOperationEvent(op, got) == nil
}
