package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

// SQLiteRemovalRecoveryEvidence is a real isolated restore result. The store
// derives every authority and original source reference independently.
type SQLiteRemovalRecoveryEvidence struct {
	DatabaseID   data.DatabaseID       `json:"database_id"`
	Verification restore.SQLiteReceipt `json:"verification"`
}

type sqliteRemovalRecovery struct {
	TaskOperationID    string                          `json:"task_operation_id"`
	ArchiveOperationID string                          `json:"archive_operation_id"`
	AttemptOperationID string                          `json:"attempt_operation_id"`
	PlanID             string                          `json:"plan_id"`
	PolicyHash         string                          `json:"policy_hash"`
	TargetHash         string                          `json:"target_hash"`
	Retention          []data.RetentionEvidence        `json:"retention"`
	Evidence           []SQLiteRemovalRecoveryEvidence `json:"evidence"`
	RecordedAt         time.Time                       `json:"recorded_at"`
}

func (s *Store) RecordSQLiteRemovalRecovery(ctx context.Context, task string, o archive.Operation, evidence []SQLiteRemovalRecoveryEvidence, hold archive.SourceHold) error {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
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
	resolving, original, err := admitRemovalCompletion(ctx, tx, p, o, task, archive.Retired)
	if err != nil || resolving.Kind != ops.SQLiteRetireResolve {
		return errors.Join(ErrConflict, err)
	}
	now := s.removalNow()
	retention, err := currentRemovalRecoveryAuthority(ctx, tx, p, guard, now)
	if err != nil {
		return err
	}
	record := sqliteRemovalRecovery{TaskOperationID: resolving.ID, ArchiveOperationID: o.ID, AttemptOperationID: original.ID, PlanID: p.ID, PolicyHash: guard.authority.Load().policy.Hash(), TargetHash: p.TargetHash, Retention: retention, Evidence: evidence, RecordedAt: now}
	if err = validateRemovalRecoveryEvidence(ctx, tx, p, record, resolving, now); err != nil {
		return err
	}
	if err = recheckRemovalOriginals(ctx, tx, p, guard); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 131072 {
		return ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_archive_recovery VALUES(?,?,?,?)", resolving.ID, o.ID, original.ID, raw); err != nil {
		return err
	}
	if err = recheckRemovalOriginals(ctx, tx, p, guard); err != nil {
		return err
	}
	return guard.commit(tx)
}

func currentRemovalRecoveryAuthority(ctx context.Context, q dataQuerier, p archive.Plan, g *SQLiteRemovalGuard, now time.Time) ([]data.RetentionEvidence, error) {
	authority := g.authority.Load()
	if authority == nil || authority.target != p.TargetHash {
		return nil, ErrConflict
	}
	raw, err := authority.policy.CanonicalBytes()
	bounds, ok := authority.policy.SQLiteRemovalBounds()
	if err != nil || !ok || bounds != p.Removal.Bounds || "sha256:"+sqliteHash(raw) != authority.policy.Hash() {
		return nil, ErrConflict
	}
	out := make([]data.RetentionEvidence, 0, len(p.Candidate.Databases))
	for _, variant := range p.Candidate.Databases {
		d, ok := variant.SQLite()
		if !ok || len(d.Epochs) != 1 || !slices.Contains(authority.policy.PersistentRoots(), string(d.Binding.Root)) {
			return nil, ErrConflict
		}
		hash, err := readSQLitePolicy(ctx, q, d.Binding.DatabaseID)
		destination, allowed := authority.policy.BackupDestination(d.Epochs[0].Destination.Reference)
		if err != nil || "sha256:"+hash != authority.policy.Hash() || !allowed || destination != d.Epochs[0].Destination {
			return nil, ErrConflict
		}
		retained, ok := authority.policy.BackupRetention(destination.Reference)
		if !ok || !retained.Admits(destination, now) {
			return nil, ErrConflict
		}
		out = append(out, retained)
	}
	return out, nil
}

func validateRemovalRecoveryEvidence(ctx context.Context, q dataQuerier, p archive.Plan, r sqliteRemovalRecovery, resolving ops.Operation, now time.Time) error {
	if r.TaskOperationID != resolving.ID || r.PlanID != p.ID || r.TargetHash != p.TargetHash || r.AttemptOperationID != resolving.RecoveryOf || r.RecordedAt.IsZero() || now.Before(r.RecordedAt) || now.Sub(r.RecordedAt) > archive.MaxEvidenceAge || len(r.Retention) != len(p.Candidate.Databases) {
		return ErrConflict
	}
	if err := checkRemovalRecoveryDeadline(ctx, q, p, r.ArchiveOperationID, now); err != nil {
		return err
	}
	count := 0
	for i, variant := range p.Candidate.Databases {
		d, ok := variant.SQLite()
		if !ok || len(d.Epochs) != 1 || !r.Retention[i].Admits(d.Epochs[0].Destination, now) {
			return ErrConflict
		}
		if !d.Written {
			continue
		}
		if count >= len(r.Evidence) || r.Evidence[count].DatabaseID != d.Binding.DatabaseID {
			return ErrConflict
		}
		if err := validateRemovalRecoveryDatabase(ctx, q, p, resolving, r.Evidence[count], r.RecordedAt, now); err != nil {
			return err
		}
		count++
	}
	if count != len(r.Evidence) {
		return ErrConflict
	}
	return nil
}

func validateRemovalRecoveryDatabase(ctx context.Context, q dataQuerier, p archive.Plan, resolving ops.Operation, e SQLiteRemovalRecoveryEvidence, recorded, now time.Time) error {
	ref, found := removalPlanRef(p, e.DatabaseID)
	if !found {
		return ErrConflict
	}
	prep, err := readSQLitePreparationCore(ctx, q, ref.PreparationID)
	if err != nil || prep.point == nil {
		return errors.Join(ErrConflict, err)
	}
	var raw []byte
	if err = q.QueryRowContext(ctx, "SELECT canonical FROM data_sqlite_point_consumers WHERE preparation_id=?", ref.PreparationID).Scan(&raw); err != nil {
		return err
	}
	consumer, err := sqliteprep.DecodeConsumer(raw)
	if err != nil || consumer.Kind != sqliteprep.ReviewedConsumer || consumer.OperationID != resolving.RecoveryOf || consumer.ReviewedPlanSHA256 != strings.TrimPrefix(p.ID, "sha256:") || consumer.Bounds == nil || *consumer.Bounds != p.Removal.Bounds || !consumer.Admits(prep.intent, *prep.point, prep.intent.Scope, consumer.ClaimedAt) || now.After(consumer.RecoverUntil) || recorded.After(consumer.RecoverUntil) {
		return ErrConflict
	}
	if err = checkRemovalPreparedHistory(ctx, q, prep); err != nil {
		return err
	}
	claim, err := readSQLiteArchiveClaim(ctx, q, ref.PreparationID)
	if err != nil || claim.TaskOperationID != resolving.RecoveryOf || claim.DatabaseID != e.DatabaseID || claim.PreparedSHA256 != ref.PreparedSHA256 {
		return &IntegrityError{}
	}
	proof := e.Verification
	original := prep.point.Verification
	if !proof.Valid() || proof.OperationID != resolving.ID+"-"+string(e.DatabaseID) || proof.DatabasePath != "" || proof.Sentinel != nil || proof.ObservedAt.Before(resolving.CreatedAt) || proof.ObservedAt.After(recorded) || now.Before(proof.ObservedAt) || now.Sub(proof.ObservedAt) > time.Duration(consumer.Bounds.MaxRestoreTestAgeSeconds)*time.Second || !reflect.DeepEqual(proof.Source, original.Source) || proof.Schema != original.Schema || proof.InvariantCheck != original.InvariantCheck || proof.Coverage == nil || !reflect.DeepEqual(proof.Coverage.Source, original.Source) || !proof.Coverage.CoveredThrough.Equal(prep.point.Source.Cutoff) || proof.LossWindow.State != restore.LossBounded || proof.ObservedAt.Before(prep.point.Source.UploadedAt) {
		return ErrConflict
	}
	return nil
}

func readSQLiteRemovalRecovery(ctx context.Context, q dataQuerier, task string) (sqliteRemovalRecovery, error) {
	var record sqliteRemovalRecovery
	var archiveOp, attempt string
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT archive_operation_id,attempt_operation_id,canonical FROM data_sqlite_archive_recovery WHERE task_operation_id=?", task).Scan(&archiveOp, &attempt, &raw)
	if err != nil {
		return record, err
	}
	if len(raw) > 131072 || json.Unmarshal(raw, &record) != nil || record.TaskOperationID != task || record.ArchiveOperationID != archiveOp || record.AttemptOperationID != attempt {
		return record, &IntegrityError{}
	}
	canonical, err := json.Marshal(record)
	if err != nil || string(canonical) != string(raw) {
		return record, &IntegrityError{}
	}
	return record, nil
}

func checkSQLiteRemovalRecoveryAtFinish(ctx context.Context, tx *sql.Tx, p archive.Plan, task ops.Operation, o archive.Operation, g *SQLiteRemovalGuard, now time.Time) error {
	record, err := readSQLiteRemovalRecovery(ctx, tx, task.ID)
	if err != nil || record.ArchiveOperationID != o.ID {
		return errors.Join(ErrConflict, err)
	}
	current, err := currentRemovalRecoveryAuthority(ctx, tx, p, g, now)
	if err != nil || !reflect.DeepEqual(current, record.Retention) || record.PolicyHash != g.authority.Load().policy.Hash() {
		return errors.Join(ErrConflict, err)
	}
	return validateRemovalRecoveryEvidence(ctx, tx, p, record, task, now)
}

func checkRemovalRecoveryDeadline(ctx context.Context, q dataQuerier, p archive.Plan, operation string, now time.Time) error {
	var at string
	if err := q.QueryRowContext(ctx, "SELECT created_at FROM data_archive_events WHERE operation_id=? AND sequence=1 AND stage='intent'", operation).Scan(&at); err != nil {
		return err
	}
	claimed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil || now.After(claimed.Add(time.Duration(p.Removal.Bounds.RecoveryWindowSeconds)*time.Second)) {
		return ErrConflict
	}
	for _, ref := range p.Removal.Points {
		var raw []byte
		if err = q.QueryRowContext(ctx, "SELECT canonical FROM data_sqlite_point_consumers WHERE preparation_id=?", ref.PreparationID).Scan(&raw); err != nil {
			return err
		}
		c, err := sqliteprep.DecodeConsumer(raw)
		if err != nil || !c.ClaimedAt.Equal(claimed) || now.After(c.RecoverUntil) {
			return ErrConflict
		}
	}
	return nil
}

func checkSQLiteRemovalRecoveryHistory(ctx context.Context, q dataQuerier, p archive.Plan, r archive.RemovalReceipt, at time.Time) error {
	record, err := readSQLiteRemovalRecovery(ctx, q, r.OperationID)
	if err != nil || record.AttemptOperationID != r.AttemptOperationID || record.PlanID != p.ID || !validSQLiteTaggedHash(record.PolicyHash) {
		return errors.Join(&IntegrityError{}, err)
	}
	task, in, err := readSQLiteRetireOperation(ctx, q, r.OperationID)
	if err != nil || task.State != ops.Succeeded || task.Kind != ops.SQLiteRetireResolve || task.RecoveryOf != r.AttemptOperationID || in.Disposition != "continue" {
		return errors.Join(&IntegrityError{}, err)
	}
	var archiveOperation string
	if err = q.QueryRowContext(ctx, "SELECT archive_operation_id FROM data_sqlite_archive_attempts WHERE task_operation_id=?", r.AttemptOperationID).Scan(&archiveOperation); err != nil || archiveOperation != record.ArchiveOperationID {
		return &IntegrityError{}
	}
	if err = checkRemovalRecoveryPolicyHistory(ctx, q, p, record.PolicyHash); err != nil {
		return err
	}
	return validateRemovalRecoveryEvidence(ctx, q, p, record, task, at)
}

func checkRemovalRecoveryPolicyHistory(ctx context.Context, q dataQuerier, p archive.Plan, hash string) error {
	for _, d := range p.Candidate.Databases {
		var count int
		err := q.QueryRowContext(ctx, "SELECT count(*) FROM data_admissions WHERE database_id=? AND policy_hash=?", d.Reference().DatabaseID, hash).Scan(&count)
		if err != nil || count == 0 {
			return errors.Join(&IntegrityError{}, err)
		}
	}
	return nil
}
