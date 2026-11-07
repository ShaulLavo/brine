package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

// ClaimAbandon records an abandonment's stop intent before any stop. It joins
// the real preflight task and input, the terminal unconsumed predecessor
// without a stopped baseline, the current policy and the original held fence
// under this store's live guard. A replica stop needs its lifetime lock held.
func (j *SQLitePreparationJournal) ClaimAbandon(ctx context.Context, stop sqliteprep.AbandonStop, observation sqliteprep.HostObservation) error {
	guard := sqliteGuardOf(observation)
	if stop.Validate() != nil || !guard.live(j.store, stop.Input.DatabaseID) {
		return ErrConflict
	}
	if stop.StopReplica {
		if err := guard.requireRunningReplica(ctx); err != nil {
			return err
		}
	}
	tx, cancel, err := j.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = claimSQLiteAbandonTx(ctx, tx, stop, guard); err != nil {
		return err
	}
	if !guard.live(j.store, stop.Input.DatabaseID) {
		return ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	j.guard = guard
	return nil
}

func claimSQLiteAbandonTx(ctx context.Context, tx *sql.Tx, stop sqliteprep.AbandonStop, guard *SQLiteMutationGuard) error {
	now := sqliteNow()
	if stop.ClaimedAt.After(now) || now.Sub(stop.ClaimedAt) > claimWindow {
		return ErrConflict
	}
	op, in, err := readSQLiteOperation(ctx, tx, stop.Task)
	if err != nil || op.State != ops.Preflight || in != stop.Input {
		return errors.Join(ErrConflict, err)
	}
	if claimed, err := sqliteClaimed(ctx, tx, op); err != nil || claimed {
		return errors.Join(ErrConflict, err)
	}
	lineage, prev, err := readSQLiteLineage(ctx, tx, in.PreparationID)
	if err != nil {
		return errors.Join(ErrConflict, err)
	}
	digest, err := sqliteprep.Fingerprint(prev.intent)
	if err != nil || digest != stop.IntentSHA256 || !lineage.Terminal || !lineage.Unconsumed || prev.intent.Task.Requester != stop.Task.Requester || prev.intent.Fence != stop.Fence || prev.intent.Scope.DatabaseID != in.DatabaseID {
		return errors.Join(ErrConflict, err)
	}
	origins, err := sqliteprep.ResolveOrigins(lineage)
	if err != nil || origins.Baseline != nil {
		return errors.Join(sqliteRefuse(sqliteprep.StoppedSourceChanged), err)
	}
	c, err := readSQLiteCurrent(ctx, tx, in, now)
	if err != nil {
		return err
	}
	if err = guard.admitUnreleased(c, stop.Task.Kind); err != nil {
		return err
	}
	if c.scope.EmptyAllocation != nil {
		c.scope.EmptyAllocation.Location = prev.intent.Location
	}
	scope, err := sqliteTaskScope(c, stop.Task.Kind, &prev.intent.Scope, nil, now)
	if err != nil || !sqliteprep.SameSourceScope(prev.intent.Scope, scope) || c.held == nil || *c.held != stop.Fence || scope.PolicySHA256 != stop.PolicySHA256 {
		return errors.Join(ErrConflict, err)
	}
	raw, err := sqliteprep.Encode(stop)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_abandon_stops VALUES(?,?,?,?)", op.ID, in.PreparationID, stop.Fence.ID, raw)
	return err
}

func sqliteAbandonStopped(ctx context.Context, q dataQuerier, op ops.Operation) (bool, error) {
	task, in, err := readSQLiteTask(ctx, q, op)
	if err != nil {
		return false, err
	}
	stop, err := readSQLiteAbandonStop(ctx, q, task, in)
	return stop != nil, err
}

// readSQLiteAbandonStop returns the task's stop intent, if any, revalidated
// against its columns, real task, input, predecessor intent and fence.
func readSQLiteAbandonStop(ctx context.Context, q dataQuerier, task sqliteprep.Task, in sqliteprep.Input) (*sqliteprep.AbandonStop, error) {
	var preparation, fence string
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT preparation_id,fence_id,canonical FROM data_sqlite_abandon_stops WHERE operation_id=?", task.OperationID).Scan(&preparation, &fence, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stop, err := sqliteprep.DecodeAbandonStop(raw)
	if err != nil || stop.Task != task || stop.Input != in || preparation != in.PreparationID || fence != string(stop.Fence.ID) {
		return nil, &IntegrityError{}
	}
	var intent []byte
	if err = q.QueryRowContext(ctx, "SELECT canonical FROM data_sqlite_preparations WHERE id=? AND fence_id=? AND database_id=?", preparation, fence, in.DatabaseID).Scan(&intent); err != nil {
		return nil, errors.Join(&IntegrityError{}, err)
	}
	previous, err := sqliteprep.DecodeIntent(intent)
	if err != nil {
		return nil, &IntegrityError{}
	}
	digest, err := sqliteprep.Fingerprint(previous)
	if err != nil || digest != stop.IntentSHA256 || previous.Fence != stop.Fence || previous.Task.Requester != task.Requester {
		return nil, &IntegrityError{}
	}
	return &stop, nil
}

// checkSQLiteAbandonStop joins completion with the task's own stop intent:
// both absent, or the identical record under an unchanged current policy.
func checkSQLiteAbandonStop(ctx context.Context, tx *sql.Tx, task sqliteprep.Task, in sqliteprep.Input, observed *sqliteprep.AbandonStop, policy string) error {
	stop, err := readSQLiteAbandonStop(ctx, tx, task, in)
	if err != nil {
		return errors.Join(sqliteRefuse(sqliteprep.InvalidEvidence), err)
	}
	if stop == nil || observed == nil {
		if stop != nil || observed != nil {
			return sqliteRefuse(sqliteprep.InvalidEvidence)
		}
		return nil
	}
	if !sqliteSameJSON(*stop, *observed) || stop.PolicySHA256 != policy {
		return sqliteRefuse(sqliteprep.InvalidEvidence)
	}
	return nil
}
