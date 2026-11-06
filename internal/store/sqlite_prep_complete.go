package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

// abandonObservationAge bounds the final local source observation of abandon.
const abandonObservationAge = time.Minute

// CompletePrepare commits the verified point, prepared event, canonical
// receipt, succeeded task and state event in one transaction while the
// store-minted guard and source hold from this task remain live.
func (j *SQLitePreparationJournal) CompletePrepare(ctx context.Context, h sqliteprep.History, p sqliteprep.PreparedPoint, held sqliteprep.StoppedSource) (sqliteprep.Receipt, error) {
	database := h.Intent.Scope.DatabaseID
	hold := sqliteHoldOf(held)
	if !hold.live(j.store, database) || hold.guard != j.guard || sqliteprep.ValidatePrepared(h, p) != nil {
		return sqliteprep.Receipt{}, ErrConflict
	}
	tx, cancel, err := j.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return sqliteprep.Receipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := completeSQLitePrepareTx(ctx, tx, h, p)
	if err != nil {
		return sqliteprep.Receipt{}, err
	}
	if !hold.live(j.store, database) {
		return sqliteprep.Receipt{}, ErrConflict
	}
	return receipt, tx.Commit()
}

func completeSQLitePrepareTx(ctx context.Context, tx *sql.Tx, h sqliteprep.History, p sqliteprep.PreparedPoint) (sqliteprep.Receipt, error) {
	i := h.Intent
	prev, err := readSQLiteAppendable(ctx, tx, i)
	if err != nil {
		return sqliteprep.Receipt{}, err
	}
	if !sqliteSameJSON(prev.events, h.Events) {
		return sqliteprep.Receipt{}, ErrConflict
	}
	now := sqliteNow()
	c, err := readSQLiteCurrent(ctx, tx, i.Task.App, i.Scope.DatabaseID, now)
	if err != nil {
		return sqliteprep.Receipt{}, err
	}
	scope, err := sqliteTaskScope(c, i.Task.Kind, &i.Scope, now)
	if err != nil || !sameSQLiteCurrent(i.Scope, scope) || c.held == nil || *c.held != i.Fence {
		return sqliteprep.Receipt{}, errors.Join(ErrConflict, err)
	}
	if err = admitSQLiteSourcePoint(ctx, tx, c, p.Source.Point); err != nil {
		return sqliteprep.Receipt{}, err
	}
	pointRaw, err := sqliteprep.Encode(p)
	if err != nil {
		return sqliteprep.Receipt{}, err
	}
	digest, err := sqliteprep.Fingerprint(p)
	if err != nil {
		return sqliteprep.Receipt{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_prepared_points VALUES(?,?,?)", p.PreparationID, p.Source.Point.ID, pointRaw); err != nil {
		return sqliteprep.Receipt{}, err
	}
	if now.Before(p.PreparedAt) {
		now = p.PreparedAt
	}
	prepared := sqliteprep.Event{PreparationID: p.PreparationID, Sequence: len(h.Events) + 1, Stage: sqliteprep.Prepared, At: now, PreparedSHA256: digest}
	if err = insertSQLiteEvent(ctx, tx, prepared); err != nil {
		return sqliteprep.Receipt{}, err
	}
	receipt := sqliteprep.Receipt{Task: i.Task, PreparationID: p.PreparationID, PreparedSHA256: digest, Fence: i.Fence, SourcePointID: p.Source.Point.ID, Outcome: sqliteprep.PreparedOutcome, CompletedAt: now}
	full := sqliteprep.History{Intent: h.Intent, Events: slices.Concat(h.Events, []sqliteprep.Event{prepared})}
	if err = sqliteprep.ValidateCompletion(full, p, receipt); err != nil {
		return sqliteprep.Receipt{}, err
	}
	return receipt, completeSQLiteTaskTx(ctx, tx, i.Task.OperationID, receipt)
}

// admitSQLiteSourcePoint reuses an existing restore-point row only when its
// canonical bytes are identical; otherwise it records the exact remote source.
func admitSQLiteSourcePoint(ctx context.Context, tx *sql.Tx, c sqliteCurrent, point data.SQLiteRestorePoint) error {
	raw, err := json.Marshal(point)
	if err != nil || point.Validate() != nil || len(raw) > 16<<10 || point.BindingID != c.scope.BindingID || point.EpochID != c.scope.EpochID || point.Schema.DatabaseID != c.scope.DatabaseID {
		return ErrConflict
	}
	if point.Snapshot != nil && point.Snapshot.ObjectKey != strings.TrimSuffix(c.permit.Replica.RemotePrefix, "/")+"/restore-points/"+point.ID+"/snapshot.sqlite" {
		return ErrConflict
	}
	var old []byte
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_restore_points WHERE id=?", point.ID).Scan(&old)
	if err == nil && bytes.Equal(old, raw) {
		return nil
	}
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return errors.Join(ErrConflict, err)
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_restore_points(id,binding_id,epoch_id,canonical) VALUES(?,?,?,?)", point.ID, point.BindingID, point.EpochID, raw)
	return err
}

func completeSQLiteTaskTx(ctx context.Context, tx *sql.Tx, id string, receipt sqliteprep.Receipt) error {
	raw, err := sqliteprep.Encode(receipt)
	if err != nil {
		return err
	}
	return completeTaskTx(ctx, tx, id, ops.Succeeded, ops.TaskOutcome{Receipt: raw})
}

// CompleteAbandon validates local settlement, records its consumer and exact
// completion, releases only the original fence and completes the task in one
// transaction. A failed local predicate is a typed no-effect refusal.
func (j *SQLitePreparationJournal) CompleteAbandon(ctx context.Context, task sqliteprep.Task, in sqliteprep.Input, a sqliteprep.Abandonment, held sqliteprep.StoppedSource) (sqliteprep.Receipt, error) {
	hold := sqliteHoldOf(held)
	now := sqliteNow()
	if !hold.live(j.store, in.DatabaseID) || task.Kind != sqliteprep.AbandonTask || a.Stopped.Validate() != nil || !a.ObservedAt.Equal(a.ObservedAt.UTC()) || a.ObservedAt.After(now) || now.Sub(a.ObservedAt) > abandonObservationAge {
		return sqliteprep.Receipt{}, sqliteRefuse(sqliteprep.LocalUncertain)
	}
	if a.OldObject != sqliteprep.ObjectUnknown && a.OldObject != sqliteprep.ObjectAbsent && a.OldObject != sqliteprep.ObjectPresent {
		return sqliteprep.Receipt{}, sqliteRefuse(sqliteprep.InvalidEvidence)
	}
	tx, cancel, err := j.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return sqliteprep.Receipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	prev, err := admitSQLiteAbandonTx(ctx, tx, task, in, a, now)
	if err != nil {
		return sqliteprep.Receipt{}, err
	}
	receipt, err := commitSQLiteAbandonTx(ctx, tx, task, prev, a.OldObject, now)
	if err != nil {
		return sqliteprep.Receipt{}, err
	}
	if !hold.live(j.store, in.DatabaseID) {
		return sqliteprep.Receipt{}, ErrConflict
	}
	return receipt, tx.Commit()
}

func admitSQLiteAbandonTx(ctx context.Context, tx *sql.Tx, task sqliteprep.Task, in sqliteprep.Input, a sqliteprep.Abandonment, now time.Time) (sqlitePreparation, error) {
	op, stored, err := readSQLiteOperation(ctx, tx, task)
	if err != nil || op.State != ops.Preflight || stored != in {
		return sqlitePreparation{}, errors.Join(sqliteRefuse(sqliteprep.InvalidEvidence), err)
	}
	lineage, prev, err := readSQLiteLineage(ctx, tx, in.PreparationID)
	if err != nil {
		return prev, errors.Join(sqliteRefuse(sqliteprep.InvalidEvidence), err)
	}
	raw, err := sqliteprep.Encode(a.Intent)
	if err != nil || !bytes.Equal(raw, prev.raw) || !lineage.Terminal || !lineage.Unconsumed || prev.intent.Task.Requester != task.Requester || prev.intent.Scope.DatabaseID != in.DatabaseID || !sameSQLitePoint(prev.point, a.Point) {
		return prev, errors.Join(sqliteRefuse(sqliteprep.InvalidEvidence), err)
	}
	c, err := readSQLiteCurrent(ctx, tx, task.App, in.DatabaseID, now)
	if err != nil {
		return prev, errors.Join(sqliteRefuse(sqliteprep.InvalidEvidence), err)
	}
	scope, err := sqliteTaskScope(c, task.Kind, &prev.intent.Scope, now)
	if err != nil || !sqliteprep.SameSourceScope(prev.intent.Scope, scope) || c.held == nil || *c.held != prev.intent.Fence {
		return prev, errors.Join(sqliteRefuse(sqliteprep.InvalidEvidence), err)
	}
	origins, err := sqliteprep.ResolveOrigins(lineage)
	if err != nil || a.Stopped.Location != prev.intent.Location || origins.Baseline != nil && !origins.Baseline.Matches(a.Stopped) {
		return prev, errors.Join(sqliteRefuse(sqliteprep.StoppedSourceChanged), err)
	}
	return prev, nil
}

func sameSQLitePoint(stored, observed *sqliteprep.PreparedPoint) bool {
	if stored == nil || observed == nil {
		return stored == nil && observed == nil
	}
	return sqliteSameJSON(*stored, *observed)
}

func commitSQLiteAbandonTx(ctx context.Context, tx *sql.Tx, task sqliteprep.Task, prev sqlitePreparation, old sqliteprep.ObjectState, now time.Time) (sqliteprep.Receipt, error) {
	f := prev.intent.Fence
	receipt := sqliteprep.Receipt{OldObject: old, Task: task, PreparationID: prev.intent.Input.PreparationID, Fence: f, Outcome: sqliteprep.AbandonedOutcome, CompletedAt: now}
	receipt.Fence.State = data.FenceReleased
	policy, err := readSQLitePolicy(ctx, tx, f.DatabaseID)
	if err != nil {
		return receipt, err
	}
	consumer := sqliteprep.Consumer{PreparationID: receipt.PreparationID, OperationID: task.OperationID, Requester: task.Requester, App: task.App, InputSHA256: task.InputSHA256, Kind: sqliteprep.AbandonConsumer, PolicySHA256: policy, ClaimedAt: now}
	if prev.point != nil {
		digest, err := sqliteprep.Fingerprint(*prev.point)
		if err != nil {
			return receipt, err
		}
		receipt.PreparedSHA256, receipt.SourcePointID, consumer.PreparedSHA256 = digest, prev.point.Source.Point.ID, digest
	}
	raw, err := sqliteprep.Encode(consumer)
	if err != nil {
		return receipt, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_point_consumers VALUES(?,?,?,NULL,?)", consumer.PreparationID, consumer.OperationID, consumer.Kind, raw); err != nil {
		return receipt, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_consumer_completions VALUES(?,?,?)", consumer.PreparationID, consumer.OperationID, f.ID); err != nil {
		return receipt, err
	}
	released, err := tx.ExecContext(ctx, "UPDATE data_fences SET state='released' WHERE id=? AND operation_id=? AND database_id=? AND incarnation_id=? AND state='held'", f.ID, f.OperationID, f.DatabaseID, f.IncarnationID)
	if err != nil {
		return receipt, err
	}
	if changed, err := released.RowsAffected(); err != nil || changed != 1 {
		return receipt, errors.Join(ErrConflict, err)
	}
	return receipt, completeSQLiteTaskTx(ctx, tx, task.OperationID, receipt)
}

// FailUnclaimed settles failed only after confirming in the same transaction
// that no preparation or consumer claim exists for the task.
func (j *SQLitePreparationJournal) FailUnclaimed(ctx context.Context, task sqliteprep.Task, code sqliteprep.Code) error {
	return j.store.settleSQLiteTask(ctx, task.OperationID, &task, ops.Failed, code)
}

// MarkRecovery records unknown claimed work. It never overwrites an outcome.
func (j *SQLitePreparationJournal) MarkRecovery(ctx context.Context, task sqliteprep.Task, code sqliteprep.Code) error {
	return j.store.settleSQLiteTask(ctx, task.OperationID, &task, ops.RecoveryRequired, code)
}

// FailUnclaimedSQLiteTask is the runner's settlement of a typed refusal whose
// handler could not record it; a claimed task refuses with a conflict.
func (s *Store) FailUnclaimedSQLiteTask(ctx context.Context, id string, code sqliteprep.Code) error {
	return s.settleSQLiteTask(ctx, id, nil, ops.Failed, code)
}

// MarkSQLiteTaskRecovery is the runner's settlement of uncertain work.
func (s *Store) MarkSQLiteTaskRecovery(ctx context.Context, id string, code sqliteprep.Code) error {
	return s.settleSQLiteTask(ctx, id, nil, ops.RecoveryRequired, code)
}

func sqliteFailureCode(code sqliteprep.Code) (result.Code, bool) {
	switch code {
	case sqliteprep.LocalUncertain, sqliteprep.StoppedSourceChanged, sqliteprep.InvalidEvidence:
		return result.Conflict, true
	case sqliteprep.CoverageUnavailable, sqliteprep.CredentialsExpired:
		return result.PolicyRefused, true
	default:
		return "", false
	}
}

func (s *Store) settleSQLiteTask(ctx context.Context, id string, task *sqliteprep.Task, state ops.State, code sqliteprep.Code) error {
	outcome := ops.TaskOutcome{Error: result.RecoveryRequired}
	if state == ops.Failed {
		failure, ok := sqliteFailureCode(code)
		if !ok {
			return ErrInvalid
		}
		outcome.Error = failure
	} else if _, ok := sqliteFailureCode(code); !ok && code != sqliteprep.EffectUnknown {
		return ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", id))
	if err != nil {
		return err
	}
	joined, _, err := readSQLiteTask(ctx, tx, op)
	if err != nil || task != nil && joined != *task {
		return errors.Join(ErrConflict, err)
	}
	claimed, err := sqliteClaimed(ctx, tx, op)
	if err != nil || state == ops.Failed && claimed {
		return errors.Join(ErrConflict, err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_task_settlements VALUES(?,?)", op.ID, code); err != nil {
		return err
	}
	if err = completeTaskTx(ctx, tx, op.ID, state, outcome); err != nil {
		return err
	}
	return tx.Commit()
}

// ReadSQLiteTaskOutcome is the runner's kind-specific committed-completion
// readback. Success requires the complete joined result; failure requires a
// protected no-claim settlement. A preflight task without outcome returns nil.
func (s *Store) ReadSQLiteTaskOutcome(ctx context.Context, id string) (*ops.TaskOutcome, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", id))
	if err != nil {
		return nil, err
	}
	task, _, err := readSQLiteTask(ctx, tx, op)
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) && !op.State.IsTerminal() {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	outcome, err := ops.DecodeTaskOutcome(op, raw)
	if err != nil {
		return nil, &IntegrityError{}
	}
	if err = checkSQLiteSettlement(ctx, tx, op, task); err != nil {
		return nil, err
	}
	return &outcome, tx.Commit()
}

func checkSQLiteSettlement(ctx context.Context, tx *sql.Tx, op ops.Operation, task sqliteprep.Task) error {
	switch op.State {
	case ops.Succeeded:
		_, err := readSQLiteSuccess(ctx, tx, op, task)
		return err
	case ops.Failed:
		var code string
		claimed, err := sqliteClaimed(ctx, tx, op)
		if err != nil || claimed {
			return errors.Join(&IntegrityError{}, err)
		}
		return tx.QueryRowContext(ctx, "SELECT code FROM data_sqlite_task_settlements WHERE operation_id=?", op.ID).Scan(&code)
	case ops.RecoveryRequired:
		return nil
	default:
		return ErrConflict
	}
}

// readSQLiteSuccess validates the immutable historical completion. Later
// legitimate consumption or fence release does not invalidate it.
func readSQLiteSuccess(ctx context.Context, tx *sql.Tx, op ops.Operation, task sqliteprep.Task) (sqliteprep.Receipt, error) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", op.ID).Scan(&raw); err != nil {
		return sqliteprep.Receipt{}, err
	}
	outcome, err := ops.DecodeTaskOutcome(op, raw)
	if err != nil {
		return sqliteprep.Receipt{}, &IntegrityError{}
	}
	receipt, err := sqliteprep.DecodeReceipt(outcome.Receipt)
	if err != nil || receipt.Task != task {
		return sqliteprep.Receipt{}, &IntegrityError{}
	}
	prev, err := readSQLitePreparation(ctx, tx, receipt.PreparationID)
	if err != nil {
		return sqliteprep.Receipt{}, errors.Join(&IntegrityError{}, err)
	}
	if task.Kind == sqliteprep.PrepareTask {
		return receipt, checkSQLitePrepared(ctx, tx, prev, receipt)
	}
	return receipt, checkSQLiteAbandoned(ctx, tx, prev, receipt)
}

func checkSQLitePrepared(ctx context.Context, tx *sql.Tx, prev sqlitePreparation, r sqliteprep.Receipt) error {
	if prev.point == nil || prev.intent.Task != r.Task || sqliteprep.ValidateCompletion(prev.history(), *prev.point, r) != nil {
		return &IntegrityError{}
	}
	var stored []byte
	raw, err := json.Marshal(prev.point.Source.Point)
	if err != nil {
		return &IntegrityError{}
	}
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_restore_points WHERE id=? AND binding_id=? AND epoch_id=?", r.SourcePointID, prev.intent.Scope.BindingID, prev.intent.Scope.EpochID).Scan(&stored); err != nil || !bytes.Equal(stored, raw) {
		return errors.Join(&IntegrityError{}, err)
	}
	return checkSQLiteFence(ctx, tx, r.Fence, "")
}

func checkSQLiteAbandoned(ctx context.Context, tx *sql.Tx, prev sqlitePreparation, r sqliteprep.Receipt) error {
	expected := prev.intent.Fence
	expected.State = data.FenceReleased
	if r.Outcome != sqliteprep.AbandonedOutcome || r.Fence != expected || !prev.consumed {
		return &IntegrityError{}
	}
	if prev.point == nil && (r.PreparedSHA256 != "" || r.SourcePointID != "") {
		return &IntegrityError{}
	}
	if prev.point != nil {
		digest, err := sqliteprep.Fingerprint(*prev.point)
		if err != nil || r.PreparedSHA256 != digest || r.SourcePointID != prev.point.Source.Point.ID {
			return &IntegrityError{}
		}
	}
	var consumer, completion int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM data_sqlite_point_consumers WHERE preparation_id=? AND operation_id=? AND kind='abandon'", r.PreparationID, r.Task.OperationID).Scan(&consumer); err != nil || consumer != 1 {
		return errors.Join(&IntegrityError{}, err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM data_sqlite_consumer_completions WHERE preparation_id=? AND operation_id=? AND fence_id=?", r.PreparationID, r.Task.OperationID, r.Fence.ID).Scan(&completion); err != nil || completion != 1 {
		return errors.Join(&IntegrityError{}, err)
	}
	return checkSQLiteFence(ctx, tx, r.Fence, data.FenceReleased)
}

// checkSQLiteFence requires the immutable fence identity, and when given, its state.
func checkSQLiteFence(ctx context.Context, tx *sql.Tx, f data.QuiescenceFence, state data.FenceState) error {
	var current data.FenceState
	err := tx.QueryRowContext(ctx, "SELECT state FROM data_fences WHERE id=? AND operation_id=? AND database_id=? AND incarnation_id=?", f.ID, f.OperationID, f.DatabaseID, f.IncarnationID).Scan(&current)
	if err != nil || state != "" && current != state {
		return errors.Join(&IntegrityError{}, err)
	}
	return nil
}
