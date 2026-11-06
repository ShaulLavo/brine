package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

// sqliteTaskStore is the protected settlement surface of SQLite preparation
// tasks. The runner never writes their outcome through generic CompleteTask.
type sqliteTaskStore interface {
	ReadSQLiteTaskOutcome(context.Context, string) (*ops.TaskOutcome, error)
	FailUnclaimedSQLiteTask(context.Context, string, sqliteprep.Code) error
	MarkSQLiteTaskRecovery(context.Context, string, sqliteprep.Code) error
}

func isSQLitePreparationTask(kind ops.Kind) bool {
	return kind == ops.SQLitePrepare || kind == ops.SQLitePrepareAbandon
}

// finishSQLiteTask inspects the joined committed completion after the handler.
// A known stored result survives a lost acknowledgement and is never
// overwritten. Unsettled work fails only when the store confirms no claim
// exists for a typed refusal; otherwise it remains recovery-required.
func (r Runner) finishSQLiteTask(ctx context.Context, original ops.Operation, receipt json.RawMessage, runErr error) error {
	journal, cancel := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
	defer cancel()
	store, ok := r.Store.(sqliteTaskStore)
	if !ok {
		return result.New(result.DependencyMissing, nil)
	}
	outcome, err := store.ReadSQLiteTaskOutcome(journal, original.ID)
	if err != nil {
		return errors.Join(runErr, err)
	}
	current, err := r.Store.GetOperation(journal, original.ID)
	if err != nil {
		return errors.Join(runErr, err)
	}
	if current.Kind != original.Kind || current.Requester != original.Requester || current.App != original.App || current.SecretRef != original.SecretRef || current.IdempotencyKey != original.IdempotencyKey {
		return result.New(result.Conflict, nil)
	}
	if current.State.IsTerminal() {
		return settledSQLiteTask(current, outcome, receipt, runErr)
	}
	if current.State != ops.Preflight || outcome != nil {
		return errors.Join(runErr, result.New(result.Conflict, nil))
	}
	if runErr == nil {
		runErr = result.New(result.InternalError, nil)
	}
	var refusal *sqliteprep.Refusal
	if errors.As(runErr, &refusal) && store.FailUnclaimedSQLiteTask(journal, original.ID, refusal.Code) == nil {
		return runErr
	}
	return errors.Join(runErr, store.MarkSQLiteTaskRecovery(journal, original.ID, sqliteprep.EffectUnknown))
}

func settledSQLiteTask(current ops.Operation, outcome *ops.TaskOutcome, receipt json.RawMessage, runErr error) error {
	if outcome == nil || !outcome.Valid(current) {
		return errors.Join(runErr, result.New(result.InternalError, nil))
	}
	if current.State == ops.Succeeded {
		if len(receipt) != 0 && !bytes.Equal(receipt, outcome.Receipt) {
			return errors.Join(runErr, result.New(result.InternalError, nil))
		}
		return runErr
	}
	if runErr == nil {
		return result.New(outcome.Error, nil)
	}
	return runErr
}
