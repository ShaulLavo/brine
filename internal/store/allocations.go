package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

// RecordAllocation persists affirmative host evidence before any writer starts.
// The store does not inspect/create files. Callers hold the host mutation lock.
func (s *Store) RecordAllocation(ctx context.Context, receipt data.AllocationReceipt) error {
	if !receipt.Valid() {
		return ErrInvalid
	}
	raw, err := json.Marshal(receipt)
	if err != nil || len(raw) > 4096 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var incarnation string
	if err = tx.QueryRowContext(ctx, "SELECT incarnation_id FROM data_databases WHERE id=?", receipt.DatabaseID).Scan(&incarnation); err != nil {
		return err
	}
	if incarnation != string(receipt.IncarnationID) {
		return ErrConflict
	}
	var history int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_writer_history WHERE database_id=?", receipt.DatabaseID).Scan(&history); err != nil {
		return err
	}
	if history != 0 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_allocations VALUES(?,?) ON CONFLICT(database_id) DO NOTHING", receipt.DatabaseID, raw); err != nil {
		return err
	}
	var existing []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_allocations WHERE database_id=?", receipt.DatabaseID).Scan(&existing); err != nil {
		return err
	}
	if !bytes.Equal(existing, raw) {
		return ErrConflict
	}
	return tx.Commit()
}
func readAllocation(ctx context.Context, q dataQuerier, database data.DatabaseID) (*data.AllocationReceipt, error) {
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT canonical FROM data_allocations a WHERE database_id=? AND NOT EXISTS(SELECT 1 FROM data_writer_history h WHERE h.database_id=a.database_id)", database).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipt data.AllocationReceipt
	if len(raw) > 4096 || json.Unmarshal(raw, &receipt) != nil || !receipt.Valid() || receipt.DatabaseID != database {
		return nil, &IntegrityError{}
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(encoded, raw) {
		return nil, &IntegrityError{}
	}
	return &receipt, nil
}
func (s *Store) ReadAllocation(ctx context.Context, database data.DatabaseID) (*data.AllocationReceipt, error) {
	return readAllocation(ctx, s.db, database)
}

// RecordWriterAttempt consumes untouched absence evidence, including unknown
// start outcomes. It does not imply that the writer or its health check succeeded.
func (s *Store) RecordWriterAttempt(ctx context.Context, incarnation data.AppIncarnationID, operationID string) error {
	if !data.ValidID(string(incarnation)) || operationID == "" || len(operationID) > 256 {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO data_writer_history SELECT id,? FROM data_databases WHERE incarnation_id=? ON CONFLICT(database_id,operation_id) DO NOTHING", operationID, incarnation)
	return err
}
