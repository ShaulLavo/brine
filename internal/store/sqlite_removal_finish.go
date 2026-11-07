package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

func (s *Store) FinishRemoval(ctx context.Context, task string, o archive.Operation, old, next archive.Disposition, hold archive.SourceHold) (archive.Disposition, archive.RemovalReceipt, error) {
	var receipt archive.RemovalReceipt
	if next.Stage != archive.Retired && next.Stage != archive.Abandoned {
		return old, receipt, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return old, receipt, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := loadArchivePlan(ctx, tx, o.PlanID)
	if err != nil || p.Removal == nil {
		return old, receipt, errors.Join(ErrConflict, err)
	}
	guard, err := removalGuardOf(hold, s, p)
	if err != nil {
		return old, receipt, err
	}
	now := s.removalNow()
	next.CommittedAt = now
	next.RetainUntil = time.Time{}
	if next.Stage == archive.Retired {
		next.RetainUntil = now.Add(30 * 24 * time.Hour)
	}
	if _, err = validateDispositionOperation(ctx, tx, o, old, next); err != nil {
		return old, receipt, err
	}
	executing, attempt, err := admitRemovalCompletion(ctx, tx, p, o, task, next.Stage)
	if err != nil {
		return old, receipt, err
	}
	if err = checkRemovalTerminalSources(ctx, tx, p, o, guard, next, executing); err != nil {
		return old, receipt, err
	}
	manifest, err := sqliteRemovalManifestHash(ctx, tx, p.ArchiveID)
	if err != nil {
		return old, receipt, err
	}
	receipt = archive.RemovalReceipt{Format: "brine-sqlite-archive-receipt-v1", OperationID: executing.ID, AttemptOperationID: attempt.ID, PlanID: p.ID, ArchiveID: p.ArchiveID, ManifestSHA256: manifest, Outcome: string(next.Stage), RetainUntil: next.RetainUntil}
	receipt.CompletionSHA256 = sqliteRemovalCompletionHash(receipt, next)
	if !receipt.Valid() {
		return old, receipt, ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_archive_completions VALUES(?,?,?,?)", p.ArchiveID, attempt.ID, executing.ID, receipt.Outcome); err != nil {
		return old, receipt, err
	}
	if next.Stage == archive.Retired {
		err = retireArchiveIdentityTx(ctx, tx, p, next)
	} else {
		err = releaseRemovalFences(ctx, tx, p, o, executing.ID)
	}
	if err != nil {
		return old, receipt, err
	}
	if err = insertDisposition(ctx, tx, next); err != nil {
		return old, receipt, err
	}
	raw, err := archive.EncodeRemovalReceipt(receipt)
	if err != nil {
		return old, receipt, err
	}
	if err = completeTaskTx(ctx, tx, executing.ID, ops.Succeeded, ops.TaskOutcome{Receipt: raw}); err != nil {
		return old, receipt, err
	}
	if next.Stage == archive.Abandoned {
		if err = recheckRemovalOriginals(ctx, tx, p, guard); err != nil {
			return old, receipt, err
		}
	}
	return next, receipt, guard.commit(tx)
}

func admitRemovalCompletion(ctx context.Context, tx *sql.Tx, p archive.Plan, o archive.Operation, id string, stage archive.DispositionStage) (ops.Operation, ops.Operation, error) {
	executing, in, err := readSQLiteRetireOperation(ctx, tx, id)
	if err != nil || executing.State != ops.Preflight || in.PlanID != p.ID {
		return executing, ops.Operation{}, errors.Join(ErrConflict, err)
	}
	var original string
	if err = tx.QueryRowContext(ctx, "SELECT task_operation_id FROM data_sqlite_archive_attempts WHERE archive_operation_id=?", o.ID).Scan(&original); err != nil {
		return executing, ops.Operation{}, err
	}
	attempt, _, err := readSQLiteRetireOperation(ctx, tx, original)
	if err != nil || attempt.Kind != ops.SQLiteRetire {
		return executing, attempt, errors.Join(ErrConflict, err)
	}
	if executing.Kind == ops.SQLiteRetire {
		if executing.ID != attempt.ID || attempt.State != ops.Preflight {
			return executing, attempt, ErrConflict
		}
		return executing, attempt, nil
	}
	decision := "continue"
	if stage == archive.Abandoned {
		decision = "abandon"
	}
	if executing.Kind != ops.SQLiteRetireResolve || executing.RecoveryOf != attempt.ID || attempt.State != ops.RecoveryRequired || in.OriginalOperationID != attempt.ID || in.Disposition != decision {
		return executing, attempt, ErrConflict
	}
	return executing, attempt, nil
}

func checkRemovalTerminalSources(ctx context.Context, tx *sql.Tx, p archive.Plan, o archive.Operation, g *SQLiteRemovalGuard, next archive.Disposition, task ops.Operation) error {
	candidate, err := readArchiveCandidateOwned(ctx, tx, p.Candidate.App, o.ID)
	if err != nil || !removalCandidateMatches(candidate, p.Candidate) {
		return errors.Join(ErrConflict, err)
	}
	if next.Stage == archive.Abandoned {
		return recheckRemovalOriginals(ctx, tx, p, g)
	}
	if task.Kind == ops.SQLiteRetireResolve {
		return checkSQLiteRemovalRecoveryAtFinish(ctx, tx, p, task, o, g, next.CommittedAt)
	}
	authority := g.authority.Load()
	if authority == nil || authority.target != p.TargetHash || authority.policy.Hash() != p.PolicyHash {
		return ErrConflict
	}
	bounds, ok := authority.policy.SQLiteRemovalBounds()
	if !ok || bounds != p.Removal.Bounds {
		return ErrConflict
	}
	for _, ref := range p.Removal.Points {
		prep, err := readSQLitePreparation(ctx, tx, ref.PreparationID)
		if err != nil || !prep.consumed || prep.point == nil {
			return errors.Join(ErrConflict, err)
		}
		if err = checkRemovalRetention(ctx, tx, p, prep, next); err != nil {
			return err
		}
	}
	return nil
}

func checkRemovalRetention(ctx context.Context, tx *sql.Tx, p archive.Plan, prep sqlitePreparation, next archive.Disposition) error {
	current, err := readSQLiteCurrent(ctx, tx, p.Candidate.App, prep.intent.Scope.DatabaseID, next.CommittedAt)
	if err != nil || current.authorization != nil || current.scope.PolicySHA256 != prep.intent.Scope.PolicySHA256 || !next.CommittedAt.Before(current.scope.Retention.FreshUntil) || current.scope.Retention.RetainUntil.Before(next.RetainUntil) {
		return errors.Join(ErrConflict, err)
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_sqlite_point_consumers WHERE preparation_id=?", prep.intent.Input.PreparationID).Scan(&raw); err != nil {
		return err
	}
	consumer, err := sqliteprep.DecodeConsumer(raw)
	if err != nil || next.CommittedAt.Before(consumer.ClaimedAt) || next.CommittedAt.Sub(consumer.ClaimedAt) > archive.MaxEvidenceAge || next.CommittedAt.After(consumer.RecoverUntil) {
		return ErrConflict
	}
	return nil
}

func recheckRemovalOriginals(ctx context.Context, q dataQuerier, p archive.Plan, g *SQLiteRemovalGuard) error {
	if err := g.Recheck(ctx); err != nil {
		return err
	}
	for _, ref := range p.Removal.Points {
		prep, err := readSQLitePreparationCore(ctx, q, ref.PreparationID)
		if err != nil || prep.point == nil {
			return errors.Join(ErrConflict, err)
		}
		if err = g.recheckPrepared(ctx, ref.DatabaseID, prep.point.Stopped); err != nil {
			return err
		}
	}
	return nil
}

func releaseRemovalFences(ctx context.Context, tx *sql.Tx, p archive.Plan, o archive.Operation, completing string) error {
	for _, f := range o.Fences {
		if err := completeRemovalConsumer(ctx, tx, p, f, completing); err != nil {
			return err
		}
		updated, err := tx.ExecContext(ctx, "UPDATE data_fences SET state='released' WHERE id=? AND operation_id=? AND database_id=? AND incarnation_id=? AND state='held'", f.ID, f.OperationID, f.DatabaseID, f.IncarnationID)
		if err != nil {
			return err
		}
		count, err := updated.RowsAffected()
		if err != nil || count != 1 {
			return errors.Join(ErrConflict, err)
		}
	}
	return nil
}

func checkSQLiteRemovalCoverage(ctx context.Context, tx *sql.Tx, o archive.Operation, p archive.Plan, c archive.SQLiteCoverage, raw []byte) error {
	ref, found := removalPlanRef(p, c.DatabaseID)
	if !found || c.Prepared == nil || c.Prepared.Ref != ref || c.Point == nil || c.Receipt == nil {
		return ErrConflict
	}
	prep, err := readSQLitePreparation(ctx, tx, ref.PreparationID)
	if err != nil || prep.point == nil || !prep.consumed {
		return errors.Join(ErrConflict, err)
	}
	if !sqliteSameJSON(prep.point.Source.Point, *c.Point) || !sqliteSameJSON(prep.point.Verification, *c.Receipt) {
		return ErrConflict
	}
	var stored, consumerRaw []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_restore_points WHERE id=? AND binding_id=? AND epoch_id=?", ref.SourcePointID, c.Point.BindingID, c.Point.EpochID).Scan(&stored); err != nil || string(raw) != string(stored) {
		return errors.Join(ErrConflict, err)
	}
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_sqlite_point_consumers WHERE preparation_id=?", ref.PreparationID).Scan(&consumerRaw); err != nil {
		return err
	}
	consumer, err := sqliteprep.DecodeConsumer(consumerRaw)
	if err != nil || !consumer.ClaimedAt.Equal(c.Prepared.ClaimedAt) || sqliteNow().After(consumer.RecoverUntil) {
		return ErrConflict
	}
	return checkSQLiteArchiveFenceOwner(ctx, tx, o.ID, ref.Fence)
}

func completeRemovalConsumer(ctx context.Context, tx *sql.Tx, p archive.Plan, f data.QuiescenceFence, completing string) error {
	ref, found := removalPlanRef(p, f.DatabaseID)
	if !found {
		return nil
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO data_sqlite_consumer_completions VALUES(?,?,?)", ref.PreparationID, completing, f.ID)
	return err
}
