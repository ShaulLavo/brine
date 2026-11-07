package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type SQLiteRetireInput struct {
	Kind                ops.Kind `json:"kind"`
	App                 string   `json:"app"`
	PlanID              string   `json:"plan_id"`
	OriginalOperationID string   `json:"original_operation_id"`
	Disposition         string   `json:"disposition"`
}

func isSQLiteRetireKind(kind ops.Kind) bool {
	return kind == ops.SQLiteRetire || kind == ops.SQLiteRetireResolve
}

func (in SQLiteRetireInput) valid() bool {
	if !validSQLiteTaggedHash(in.PlanID) || !ops.ValidIntent(ops.Intent{Kind: in.Kind, App: in.App, SecretRef: sqliteHash(nil), RecoveryOf: in.OriginalOperationID}) {
		return false
	}
	if in.Kind == ops.SQLiteRetire {
		return in.Disposition == "" && in.OriginalOperationID == ""
	}
	return in.Kind == ops.SQLiteRetireResolve && (in.Disposition == "continue" || in.Disposition == "abandon")
}

func (s *Store) LookupRemovalPlan(ctx context.Context, id string) (archive.Plan, bool, error) {
	p, err := loadArchivePlan(ctx, s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return archive.Plan{}, false, nil
	}
	if err != nil {
		return archive.Plan{}, false, err
	}
	return p, p.Removal != nil, nil
}

func (s *Store) SaveSQLiteRetireInput(ctx context.Context, in SQLiteRetireInput) (string, error) {
	if s.readOnly || !in.valid() {
		return "", ErrInvalid
	}
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > sqliteTaskInputLimit {
		return "", ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := loadArchivePlan(ctx, tx, in.PlanID)
	if err != nil || p.Removal == nil || p.Candidate.App != in.App {
		return "", errors.Join(ErrConflict, err)
	}
	if in.Kind == ops.SQLiteRetire && !s.removalNow().Before(p.Removal.ExpiresAt) {
		return "", ErrConflict
	}
	if in.Kind == ops.SQLiteRetireResolve {
		if _, err = readSQLiteRetireOriginal(ctx, tx, p, in.OriginalOperationID); err != nil {
			return "", err
		}
	}
	id := sqliteHash(raw)
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_retire_inputs VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING", id, in.Kind, in.App, in.PlanID, in.OriginalOperationID, in.Disposition, raw); err != nil {
		return "", err
	}
	var stored []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_sqlite_retire_inputs WHERE id=?", id).Scan(&stored); err != nil || !bytes.Equal(stored, raw) {
		return "", errors.Join(ErrConflict, err)
	}
	return id, tx.Commit()
}

func readSQLiteRetireOperation(ctx context.Context, q dataQuerier, id string) (ops.Operation, SQLiteRetireInput, error) {
	op, err := scanOperation(q.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", id))
	if err != nil {
		return op, SQLiteRetireInput{}, err
	}
	in, err := readSQLiteRetireInput(ctx, q, op)
	return op, in, err
}

func readSQLiteRetireInput(ctx context.Context, q dataQuerier, op ops.Operation) (SQLiteRetireInput, error) {
	var in SQLiteRetireInput
	if !isSQLiteRetireKind(op.Kind) {
		return in, ErrConflict
	}
	var columns SQLiteRetireInput
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT kind,app,plan_id,original_operation_id,disposition,canonical FROM data_sqlite_retire_inputs WHERE id=?", op.SecretRef).Scan(&columns.Kind, &columns.App, &columns.PlanID, &columns.OriginalOperationID, &columns.Disposition, &raw)
	if err != nil {
		return in, err
	}
	if len(raw) > sqliteTaskInputLimit || json.Unmarshal(raw, &in) != nil || !in.valid() || in != columns {
		return in, &IntegrityError{}
	}
	canonical, err := json.Marshal(in)
	if err != nil || !bytes.Equal(raw, canonical) || sqliteHash(raw) != op.SecretRef {
		return in, &IntegrityError{}
	}
	if in.Kind != op.Kind || in.App != op.App || in.OriginalOperationID != op.RecoveryOf {
		return in, ErrConflict
	}
	p, err := loadArchivePlan(ctx, q, in.PlanID)
	if err != nil || p.Removal == nil || p.Candidate.App != op.App || p.Requester != op.Requester {
		return in, errors.Join(ErrConflict, err)
	}
	return in, nil
}

func (s *Store) ReadSQLiteRetireInput(ctx context.Context, id string) (SQLiteRetireInput, error) {
	_, in, err := readSQLiteRetireOperation(ctx, s.db, id)
	return in, err
}

func readSQLiteRetireOriginal(ctx context.Context, q dataQuerier, p archive.Plan, id string) (ops.Operation, error) {
	op, in, err := readSQLiteRetireOperation(ctx, q, id)
	if err != nil || op.Kind != ops.SQLiteRetire || op.State != ops.RecoveryRequired || in.PlanID != p.ID || op.App != p.Candidate.App || op.Requester != p.Requester {
		return op, errors.Join(ErrConflict, err)
	}
	var count int
	err = q.QueryRowContext(ctx, "SELECT count(*) FROM data_sqlite_archive_attempts a JOIN data_archive_operations o ON o.id=a.archive_operation_id WHERE o.plan_id=? AND a.task_operation_id=?", p.ID, id).Scan(&count)
	if err != nil || count != 1 {
		return op, errors.Join(ErrConflict, err)
	}
	return op, nil
}

func (s *Store) ReadSQLiteRetireSuccess(ctx context.Context, task ops.Operation) (archive.RemovalReceipt, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return archive.RemovalReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	op, in, err := readSQLiteRetireOperation(ctx, tx, task.ID)
	if err != nil || !sameSQLiteRetireTask(op, task) {
		return archive.RemovalReceipt{}, false, errors.Join(ErrConflict, err)
	}
	if op.State != ops.Succeeded {
		return archive.RemovalReceipt{}, false, tx.Commit()
	}
	r, err := readSQLiteRetireSuccess(ctx, tx, op, in)
	if err != nil {
		return r, false, err
	}
	return r, true, tx.Commit()
}

func sameSQLiteRetireTask(a, b ops.Operation) bool {
	return a.ID == b.ID && a.Kind == b.Kind && a.App == b.App && a.Requester == b.Requester && a.SecretRef == b.SecretRef && a.RecoveryOf == b.RecoveryOf && a.IdempotencyKey == b.IdempotencyKey && a.PlanID == b.PlanID
}

func readSQLiteRetireSuccess(ctx context.Context, q dataQuerier, op ops.Operation, in SQLiteRetireInput) (archive.RemovalReceipt, error) {
	var raw []byte
	if err := q.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", op.ID).Scan(&raw); err != nil {
		return archive.RemovalReceipt{}, err
	}
	outcome, err := ops.DecodeTaskOutcome(op, raw)
	if err != nil {
		return archive.RemovalReceipt{}, &IntegrityError{}
	}
	r, err := archive.DecodeRemovalReceipt(outcome.Receipt)
	if err != nil || r.OperationID != op.ID || r.PlanID != in.PlanID || op.Kind == ops.SQLiteRetireResolve && in.Disposition != map[string]string{"retired": "continue", "abandoned": "abandon"}[r.Outcome] {
		return r, &IntegrityError{}
	}
	var archiveID, attempt, completing, decision string
	err = q.QueryRowContext(ctx, "SELECT archive_id,attempt_operation_id,completing_operation_id,outcome FROM data_sqlite_archive_completions WHERE completing_operation_id=?", op.ID).Scan(&archiveID, &attempt, &completing, &decision)
	if err != nil || string(r.ArchiveID) != archiveID || r.AttemptOperationID != attempt || r.OperationID != completing || r.Outcome != decision || op.Kind == ops.SQLiteRetire && attempt != op.ID || op.Kind == ops.SQLiteRetireResolve && attempt != op.RecoveryOf {
		return r, errors.Join(&IntegrityError{}, err)
	}
	p, err := loadArchivePlan(ctx, q, in.PlanID)
	if err != nil {
		return r, err
	}
	return r, checkSQLiteRemovalCompletion(ctx, q, p, r)
}

func (s *Store) FailUnclaimedSQLiteRetire(ctx context.Context, task ops.Operation, code string) error {
	return s.settleSQLiteRetire(ctx, task, ops.Failed, code)
}

func (s *Store) MarkSQLiteRetireRecovery(ctx context.Context, task ops.Operation, code string) error {
	return s.settleSQLiteRetire(ctx, task, ops.RecoveryRequired, code)
}

func (s *Store) settleSQLiteRetire(ctx context.Context, task ops.Operation, state ops.State, code string) error {
	if code == "" || len(code) > 128 || state != ops.Failed && state != ops.RecoveryRequired {
		return ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	op, in, err := readSQLiteRetireOperation(ctx, tx, task.ID)
	if err != nil || !sameSQLiteRetireTask(op, task) || op.State != ops.Preflight {
		return errors.Join(ErrConflict, err)
	}
	var claimed int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_archive_operations WHERE plan_id=?", in.PlanID).Scan(&claimed)
	if err != nil || state == ops.Failed && (claimed != 0 || op.Kind != ops.SQLiteRetire) {
		return errors.Join(ErrConflict, err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_retire_settlements VALUES(?,?)", op.ID, code); err != nil {
		return err
	}
	failure := result.RecoveryRequired
	if state == ops.Failed {
		failure = result.Conflict
	}
	if err = completeTaskTx(ctx, tx, op.ID, state, ops.TaskOutcome{Error: failure}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReadSQLiteRetireOutcome(ctx context.Context, id string) (*ops.TaskOutcome, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	op, in, err := readSQLiteRetireOperation(ctx, tx, id)
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
	if op.State == ops.Succeeded {
		if _, err = readSQLiteRetireSuccess(ctx, tx, op, in); err != nil {
			return nil, err
		}
	} else if err = checkSQLiteRetireSettlement(ctx, tx, op, in); err != nil {
		return nil, err
	}
	return &outcome, tx.Commit()
}

func checkSQLiteRetireSettlement(ctx context.Context, q dataQuerier, op ops.Operation, in SQLiteRetireInput) error {
	var code string
	if err := q.QueryRowContext(ctx, "SELECT code FROM data_sqlite_retire_settlements WHERE operation_id=?", op.ID).Scan(&code); err != nil {
		return err
	}
	if code == "" || op.State != ops.Failed && op.State != ops.RecoveryRequired {
		return &IntegrityError{}
	}
	if op.State != ops.Failed {
		return nil
	}
	var claimed int
	err := q.QueryRowContext(ctx, "SELECT count(*) FROM data_archive_operations WHERE plan_id=?", in.PlanID).Scan(&claimed)
	if err != nil || claimed != 0 || op.Kind != ops.SQLiteRetire {
		return errors.Join(&IntegrityError{}, err)
	}
	return nil
}

// strictSQLiteRetireObject is shared by canonical protected claim decoders.
func strictSQLiteRetireObject(raw []byte, fields ...string) bool {
	_, err := strictjson.Object(raw, fields...)
	return len(raw) <= 4096 && err == nil
}

func (s *Store) removalNow() time.Time {
	if s.removalClock != nil {
		return s.removalClock().UTC()
	}
	return time.Now().UTC()
}
