package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/ops"
)

// AdmitRemovalDisposition checks the executing task before disposition effects.
// Completion repeats the same protected joins in its atomic transaction.
func (s *Store) AdmitRemovalDisposition(ctx context.Context, task string, o archive.Operation, action archive.RemovalAction, hold archive.SourceHold) error {
	stage := archive.Retired
	switch action {
	case archive.RemovalDirectRetire, archive.RemovalContinue:
	case archive.RemovalAbandon:
		stage = archive.Abandoned
	default:
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := loadArchivePlan(ctx, tx, o.PlanID)
	if err != nil || p.Removal == nil {
		return errors.Join(ErrConflict, err)
	}
	guard, err := removalGuardOf(hold, s, p)
	if err != nil {
		return err
	}
	current, exists, err := readArchiveOperation(ctx, tx, p.ID)
	if err != nil || !exists || !reflect.DeepEqual(current, o) {
		return errors.Join(ErrConflict, err)
	}
	executing, _, err := admitRemovalCompletion(ctx, tx, p, o, task, stage)
	if err != nil || !removalActionAdmitsTask(action, executing.Kind) {
		return errors.Join(ErrConflict, err)
	}
	return guard.commit(tx)
}

func removalActionAdmitsTask(action archive.RemovalAction, kind ops.Kind) bool {
	switch action {
	case archive.RemovalDirectRetire:
		return kind == ops.SQLiteRetire
	case archive.RemovalContinue:
		return kind == ops.SQLiteRetireResolve
	case archive.RemovalAbandon:
		return kind == ops.SQLiteRetire || kind == ops.SQLiteRetireResolve
	default:
		return false
	}
}
