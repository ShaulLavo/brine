package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

// This reader never follows a preparation's consumer back into its own source.
func (prep sqlitePreparation) validateArchiveConsumer(ctx context.Context, q dataQuerier, c sqliteprep.Consumer) error {
	claim, err := readSQLiteArchiveClaim(ctx, q, prep.intent.Input.PreparationID)
	if err != nil {
		return errors.Join(&IntegrityError{}, err)
	}
	op, in, err := readSQLiteRetireOperation(ctx, q, c.OperationID)
	if err != nil || op.Kind != ops.SQLiteRetire || op.State != ops.Preflight && op.State != ops.RecoveryRequired && op.State != ops.Succeeded || claim.TaskOperationID != op.ID {
		return errors.Join(&IntegrityError{}, err)
	}
	p, err := loadArchivePlan(ctx, q, in.PlanID)
	if err != nil || p.Removal == nil {
		return errors.Join(&IntegrityError{}, err)
	}
	ref, ok := removalPlanRef(p, prep.intent.Scope.DatabaseID)
	expected, err := removalRef(prep)
	if !ok || err != nil || !reflect.DeepEqual(ref, expected) || claim.DatabaseID != ref.DatabaseID || claim.FenceID != ref.Fence.ID || claim.OriginalFenceOwner != ref.Fence.OperationID || claim.PreparedSHA256 != ref.PreparedSHA256 || c.PreparedSHA256 != ref.PreparedSHA256 || c.PolicySHA256 != strings.TrimPrefix(p.PolicyHash, "sha256:") || c.ReviewedPlanSHA256 != strings.TrimPrefix(p.ID, "sha256:") || c.RetentionSHA256 != ref.RetentionSHA256 || c.Bounds == nil || *c.Bounds != p.Removal.Bounds || !c.ClaimedAt.Before(ref.ExpiresAt) || !c.ClaimedAt.Before(p.Removal.ExpiresAt) {
		return &IntegrityError{}
	}
	if !c.Admits(prep.intent, *prep.point, prep.intent.Scope, c.ClaimedAt) || c.ClaimedAt.Before(p.CreatedAt) {
		return &IntegrityError{}
	}
	if err = checkRemovalPreparedHistory(ctx, q, prep); err != nil {
		return err
	}
	var operation, at string
	var mapped int
	err = q.QueryRowContext(ctx, "SELECT a.id,e.created_at,(SELECT count(*) FROM data_archive_fences f WHERE f.operation_id=a.id AND f.database_id=? AND f.fence_id=?) FROM data_archive_operations a JOIN data_archive_events e ON e.operation_id=a.id AND e.sequence=1 AND e.stage='intent' WHERE a.plan_id=?", ref.DatabaseID, ref.Fence.ID, p.ID).Scan(&operation, &at, &mapped)
	created, parseErr := time.Parse(time.RFC3339Nano, at)
	if err != nil || parseErr != nil || operation != claim.ArchiveOperationID || mapped != 1 || !created.Equal(c.ClaimedAt) {
		return errors.Join(&IntegrityError{}, err)
	}
	return validateSQLiteArchiveConsumerCompletion(ctx, q, p, prep, claim, op)
}

func validateSQLiteArchiveConsumerCompletion(ctx context.Context, q dataQuerier, p archive.Plan, prep sqlitePreparation, claim sqliteArchiveClaim, op ops.Operation) error {
	var completing, attempt, outcome string
	err := q.QueryRowContext(ctx, "SELECT attempt_operation_id,completing_operation_id,outcome FROM data_sqlite_archive_completions WHERE archive_id=?", p.ArchiveID).Scan(&attempt, &completing, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		if op.State == ops.Succeeded {
			return &IntegrityError{}
		}
		return checkSQLiteRemovalFence(ctx, q, prep.intent.Fence, data.FenceHeld)
	}
	if err != nil || attempt != op.ID {
		return errors.Join(&IntegrityError{}, err)
	}
	fin, input, err := readSQLiteRetireOperation(ctx, q, completing)
	if err != nil || fin.State != ops.Succeeded || input.PlanID != p.ID || fin.ID != op.ID && (fin.Kind != ops.SQLiteRetireResolve || fin.RecoveryOf != op.ID || op.State != ops.RecoveryRequired) {
		return errors.Join(&IntegrityError{}, err)
	}
	var raw []byte
	if err = q.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", fin.ID).Scan(&raw); err != nil {
		return err
	}
	result, err := ops.DecodeTaskOutcome(fin, raw)
	if err != nil {
		return &IntegrityError{}
	}
	receipt, err := archive.DecodeRemovalReceipt(result.Receipt)
	if err != nil || receipt.AttemptOperationID != op.ID || receipt.OperationID != fin.ID || receipt.PlanID != p.ID || receipt.ArchiveID != p.ArchiveID || receipt.Outcome != outcome {
		return &IntegrityError{}
	}
	if err = checkSQLiteRemovalTerminalReceipt(ctx, q, p, receipt); err != nil {
		return err
	}
	if outcome == string(archive.Retired) {
		return checkSQLiteRemovalFence(ctx, q, prep.intent.Fence, data.FenceHeld)
	}
	if outcome != string(archive.Abandoned) {
		return &IntegrityError{}
	}
	var count int
	err = q.QueryRowContext(ctx, "SELECT count(*) FROM data_sqlite_consumer_completions WHERE preparation_id=? AND operation_id=? AND fence_id=?", claim.PreparationID, fin.ID, claim.FenceID).Scan(&count)
	if err != nil || count != 1 {
		return errors.Join(&IntegrityError{}, err)
	}
	return checkSQLiteRemovalFence(ctx, q, prep.intent.Fence, data.FenceReleased)
}

func checkSQLiteRemovalFence(ctx context.Context, q dataQuerier, f data.QuiescenceFence, state data.FenceState) error {
	var actual data.FenceState
	err := q.QueryRowContext(ctx, "SELECT state FROM data_fences WHERE id=? AND database_id=? AND incarnation_id=? AND operation_id=?", f.ID, f.DatabaseID, f.IncarnationID, f.OperationID).Scan(&actual)
	if err != nil || actual != state {
		return errors.Join(&IntegrityError{}, err)
	}
	return nil
}

func (s *Store) SQLiteArchiveFence(ctx context.Context, archiveID data.ArchiveID, database data.DatabaseID) (data.QuiescenceFence, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := loadArchivePlanByArchive(ctx, tx, archiveID)
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	o, exists, err := readArchiveOperation(ctx, tx, p.ID)
	if err != nil || !exists {
		return data.QuiescenceFence{}, errors.Join(ErrConflict, err)
	}
	for _, f := range o.Fences {
		if f.DatabaseID == database {
			return f, tx.Commit()
		}
	}
	return data.QuiescenceFence{}, ErrNotFound
}

func checkSQLiteRemovalCompletion(ctx context.Context, q dataQuerier, p archive.Plan, r archive.RemovalReceipt) error {
	var attempt string
	if err := q.QueryRowContext(ctx, "SELECT a.task_operation_id FROM data_sqlite_archive_attempts a JOIN data_archive_operations o ON o.id=a.archive_operation_id WHERE o.plan_id=?", p.ID).Scan(&attempt); err != nil || attempt != r.AttemptOperationID {
		return errors.Join(&IntegrityError{}, err)
	}
	original, input, err := readSQLiteRetireOperation(ctx, q, attempt)
	if err != nil || original.Kind != ops.SQLiteRetire || input.PlanID != p.ID || r.OperationID == attempt && original.State != ops.Succeeded || r.OperationID != attempt && original.State != ops.RecoveryRequired {
		return errors.Join(&IntegrityError{}, err)
	}
	if err = checkSQLiteRemovalTerminalReceipt(ctx, q, p, r); err != nil {
		return err
	}

	for _, ref := range p.Removal.Points {
		prep, err := readSQLitePreparation(ctx, q, ref.PreparationID)
		if err != nil || !prep.consumed {
			return errors.Join(&IntegrityError{}, err)
		}
	}
	return nil
}

func sqliteRemovalManifestHash(ctx context.Context, q dataQuerier, id data.ArchiveID) (string, error) {
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT canonical FROM data_archive_manifests WHERE archive_id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if _, err = readArchiveManifest(ctx, q, id); err != nil {
		return "", err
	}
	return sqliteHash(raw), nil
}

func sqliteRemovalCompletionHash(r archive.RemovalReceipt, d archive.Disposition) string {
	r.CompletionSHA256 = ""
	raw, err := json.Marshal(struct {
		Receipt     archive.RemovalReceipt `json:"receipt"`
		Disposition archive.Disposition    `json:"disposition"`
	}{r, d})
	if err != nil {
		return ""
	}
	return sqliteHash(raw)
}

// ReadSQLiteRemovalPoint returns joined immutable evidence, never a mutation
// permit. CoverPrepared must retain the registered aggregate independently.
func (s *Store) ReadSQLiteRemovalPoint(ctx context.Context, planID string, database data.DatabaseID) (sqliteprep.PreparedPoint, time.Time, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return sqliteprep.PreparedPoint{}, time.Time{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := loadArchivePlan(ctx, tx, planID)
	if err != nil || p.Removal == nil {
		return sqliteprep.PreparedPoint{}, time.Time{}, errors.Join(ErrConflict, err)
	}
	ref, found := removalPlanRef(p, database)
	if !found {
		return sqliteprep.PreparedPoint{}, time.Time{}, ErrNotFound
	}
	prep, err := readSQLitePreparation(ctx, tx, ref.PreparationID)
	if err != nil || prep.point == nil || !prep.consumed {
		return sqliteprep.PreparedPoint{}, time.Time{}, errors.Join(ErrConflict, err)
	}
	expected, err := removalRef(prep)
	if err != nil || expected != ref {
		return sqliteprep.PreparedPoint{}, time.Time{}, ErrConflict
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_sqlite_point_consumers WHERE preparation_id=?", ref.PreparationID).Scan(&raw); err != nil {
		return sqliteprep.PreparedPoint{}, time.Time{}, err
	}
	c, err := sqliteprep.DecodeConsumer(raw)
	if err != nil {
		return sqliteprep.PreparedPoint{}, time.Time{}, &IntegrityError{}
	}
	return *prep.point, c.ClaimedAt, tx.Commit()
}

func readSQLiteRetiredScope(ctx context.Context, q dataQuerier, scope sqliteprep.Scope) error {
	var preparation string
	err := q.QueryRowContext(ctx, `SELECT c.preparation_id FROM data_sqlite_archive_claims c
 JOIN data_archive_operations a ON a.id=c.archive_operation_id
 JOIN data_archive_plans p ON p.id=a.plan_id JOIN data_archive_retirements r ON r.archive_id=p.archive_id
 JOIN data_sqlite_archive_completions done ON done.archive_id=p.archive_id AND done.outcome='retired'
 WHERE c.database_id=? AND a.incarnation_id=? AND r.app=? AND done.attempt_operation_id=c.task_operation_id`, scope.DatabaseID, scope.IncarnationID, scope.App).Scan(&preparation)
	if err != nil {
		return err
	}
	prep, err := readSQLitePreparation(ctx, q, preparation)
	if err != nil || !sqliteprep.SameLineageScope(prep.intent.Scope, scope) || !prep.consumed {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func checkSQLiteRemovalTerminalReceipt(ctx context.Context, q dataQuerier, p archive.Plan, r archive.RemovalReceipt) error {
	d, exists, err := readArchiveDisposition(ctx, q, p.ArchiveID)
	if err != nil || !exists || string(d.Stage) != r.Outcome || !d.RetainUntil.Equal(r.RetainUntil) {
		return errors.Join(&IntegrityError{}, err)
	}
	manifest, err := sqliteRemovalManifestHash(ctx, q, p.ArchiveID)
	if err != nil || manifest != r.ManifestSHA256 || r.CompletionSHA256 != sqliteRemovalCompletionHash(r, d) {
		return errors.Join(&IntegrityError{}, err)
	}
	if r.Outcome == string(archive.Retired) && r.OperationID != r.AttemptOperationID {
		return checkSQLiteRemovalRecoveryHistory(ctx, q, p, r, d.CommittedAt)
	}
	return nil
}
