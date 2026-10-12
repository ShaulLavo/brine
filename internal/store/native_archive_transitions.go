package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

// NativeArchiveTransition describes one retained terminal transition. Reading
// this closure grants no movement or retirement authority.
type NativeArchiveTransition struct {
	Input  data.EngineTransition
	Cursor data.EngineTransitionCursor
}

func (s *Store) ReadNativeArchiveTransitionClosure(ctx context.Context, binding data.ReplicaBindingID) ([]NativeArchiveTransition, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	closed, err := readNativeArchiveTransitionClosureTx(ctx, tx, binding)
	if err != nil {
		return nil, err
	}
	return closed, tx.Commit()
}

func readNativeArchiveTransitionClosureTx(ctx context.Context, tx *sql.Tx, binding data.ReplicaBindingID) ([]NativeArchiveTransition, error) {
	reserved, err := readReservedEngine(ctx, tx, binding)
	if err != nil {
		return nil, err
	}
	var pending int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_native_repository_intents i LEFT JOIN data_native_epochs e ON e.epoch_id=i.epoch_id AND e.binding_id=i.binding_id WHERE i.binding_id=? AND e.epoch_id IS NULL", binding).Scan(&pending)
	if err != nil || pending != 0 {
		return nil, errors.Join(ErrConflict, err)
	}
	ids, err := nativeArchiveTransitionIDs(ctx, tx, binding)
	if err != nil {
		return nil, err
	}
	closed := make([]NativeArchiveTransition, len(ids))
	for index, id := range ids {
		input, cursor, err := readEngineTransition(ctx, tx, id)
		if err != nil || !transitionReservedIdentity(input, reserved) || cursor.Stage != data.EngineTransitionCompleted && cursor.Stage != data.EngineTransitionAbandoned {
			return nil, errors.Join(ErrConflict, err)
		}
		epoch, archive, err := readNativeEpoch(ctx, tx, binding, input.Common().NewEpochID)
		if err != nil || !epoch.Matches(input) || archive.Validate(reserved.Database) != nil {
			return nil, errors.Join(&IntegrityError{}, err)
		}
		closed[index] = NativeArchiveTransition{Input: input, Cursor: cursor}
	}
	return closed, nil
}

func nativeArchiveTransitionIDs(ctx context.Context, tx *sql.Tx, binding data.ReplicaBindingID) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM data_engine_transition_inputs WHERE binding_id=? ORDER BY id", binding)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		if !data.ValidID(id) || len(ids) == 4096 {
			return nil, &IntegrityError{}
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
