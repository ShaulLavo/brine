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
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

type sqliteArchiveClaim struct {
	PreparationID      string          `json:"preparation_id"`
	ArchiveOperationID string          `json:"archive_operation_id"`
	TaskOperationID    string          `json:"task_operation_id"`
	DatabaseID         data.DatabaseID `json:"database_id"`
	FenceID            data.FenceID    `json:"fence_id"`
	OriginalFenceOwner string          `json:"original_fence_owner"`
	PreparedSHA256     string          `json:"prepared_sha256"`
}

func removalGuardOf(hold archive.SourceHold, s *Store, p archive.Plan) (*SQLiteRemovalGuard, error) {
	guard, ok := hold.(*SQLiteRemovalGuard)
	if !ok || !guard.live(s) || len(guard.children) != len(p.Candidate.Databases) {
		return nil, ErrConflict
	}
	for i, child := range guard.children {
		if child.guard.database != p.Candidate.Databases[i].Reference().DatabaseID || child.guard.binding != p.Candidate.Databases[i].Reference().BindingID {
			return nil, ErrConflict
		}
	}
	if !guard.sourcesLive() {
		return nil, ErrConflict
	}
	return guard, nil
}

func (s *Store) ClaimRemoval(ctx context.Context, p archive.Plan, task string, hold archive.SourceHold) (archive.Operation, error) {
	var o archive.Operation
	if !p.Valid() || p.Removal == nil {
		return o, ErrInvalid
	}
	guard, err := removalGuardOf(hold, s, p)
	if err != nil {
		return o, err
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return o, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.removalNow()
	op, err := admitRemovalTask(ctx, tx, p, task, guard, now)
	if err != nil {
		return o, err
	}
	candidate, refs, err := readRemovalCandidate(ctx, tx, p.Candidate.App, now)
	if err != nil || !removalCandidateMatches(candidate, p.Candidate) || !reflect.DeepEqual(refs, p.Removal.Points) {
		return o, errors.Join(ErrConflict, err)
	}
	o = archive.Operation{PlanID: p.ID, ArchiveID: p.ArchiveID, Stage: archive.Intent, Sequence: 1, Fences: []data.QuiescenceFence{}}
	if o.ID, err = data.NewID(); err != nil {
		return o, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_archive_operations VALUES(?,?,?)", o.ID, p.ID, p.Candidate.Incarnation); err != nil {
		return o, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_archive_attempts VALUES(?,?)", o.ID, op.ID); err != nil {
		return archive.Operation{}, err
	}
	for _, variant := range p.Candidate.Databases {
		database, _ := variant.SQLite()
		f, err := claimRemovalDatabase(ctx, tx, p, o, op, database, guard, now)
		if err != nil {
			return archive.Operation{}, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO data_archive_fences VALUES(?,?,?)", o.ID, f.DatabaseID, f.ID); err != nil {
			return archive.Operation{}, err
		}
		o.Fences = append(o.Fences, f)
	}
	if !s.removalNow().Before(p.Removal.ExpiresAt) {
		return archive.Operation{}, ErrConflict
	}
	if err = guard.Recheck(ctx); err != nil {
		return archive.Operation{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_archive_events VALUES(?,?,?,?,NULL)", o.ID, o.Sequence, o.Stage, now.Format(time.RFC3339Nano)); err != nil {
		return archive.Operation{}, err
	}
	return o, tx.Commit()
}

func admitRemovalTask(ctx context.Context, tx *sql.Tx, p archive.Plan, task string, guard *SQLiteRemovalGuard, now time.Time) (ops.Operation, error) {
	op, in, err := readSQLiteRetireOperation(ctx, tx, task)
	if err != nil || op.Kind != ops.SQLiteRetire || op.State != ops.Preflight || in.PlanID != p.ID {
		return op, errors.Join(ErrConflict, err)
	}
	stored, err := loadArchivePlan(ctx, tx, p.ID)
	if err != nil || !reflect.DeepEqual(stored, p) || !now.Before(p.Removal.ExpiresAt) {
		return op, errors.Join(ErrConflict, err)
	}
	authority := guard.authority.Load()
	if authority == nil || authority.target != p.TargetHash || authority.policy.Hash() != p.PolicyHash {
		return op, ErrConflict
	}
	bounds, ok := authority.policy.SQLiteRemovalBounds()
	if !ok || bounds != p.Removal.Bounds {
		return op, ErrConflict
	}
	raw, err := authority.policy.CanonicalBytes()
	if err != nil || "sha256:"+sqliteHash(raw) != p.PolicyHash {
		return op, ErrConflict
	}
	if err = checkRemovalAuthority(p, guard, now); err != nil {
		return op, err
	}
	var competing int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM operations WHERE kind='sqlite_retire' AND secret_ref=? AND state='preflight'", op.SecretRef).Scan(&competing)
	if err != nil || competing != 1 {
		return op, errors.Join(ErrConflict, err)
	}
	return op, nil
}

func removalPlanRef(p archive.Plan, id data.DatabaseID) (archive.PreparedRef, bool) {
	if p.Removal == nil {
		return archive.PreparedRef{}, false
	}
	for _, ref := range p.Removal.Points {
		if ref.DatabaseID == id {
			return ref, true
		}
	}
	return archive.PreparedRef{}, false
}

func claimRemovalDatabase(ctx context.Context, tx *sql.Tx, p archive.Plan, o archive.Operation, task ops.Operation, d archive.SQLiteDatabase, guard *SQLiteRemovalGuard, now time.Time) (data.QuiescenceFence, error) {
	ref, found := removalPlanRef(p, d.Binding.DatabaseID)
	if !found {
		return claimEmptyRemovalDatabase(ctx, tx, p, o, d, guard)
	}
	prep, err := readSQLitePreparation(ctx, tx, ref.PreparationID)
	if err != nil || prep.consumed || prep.point == nil {
		return data.QuiescenceFence{}, errors.Join(ErrConflict, err)
	}
	scope, err := removalCurrentScope(ctx, tx, p, d, prep, now)
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	consumer := sqliteprep.Consumer{PreparationID: ref.PreparationID, OperationID: task.ID, Requester: task.Requester, App: task.App, InputSHA256: task.SecretRef, Kind: sqliteprep.ReviewedConsumer, PreparedSHA256: ref.PreparedSHA256, ReviewedPlanSHA256: strings.TrimPrefix(p.ID, "sha256:"), PolicySHA256: scope.PolicySHA256, RetentionSHA256: ref.RetentionSHA256, Bounds: &p.Removal.Bounds, ClaimedAt: now, RecoverUntil: now.Add(time.Duration(p.Removal.Bounds.RecoveryWindowSeconds) * time.Second)}
	if !consumer.Admits(prep.intent, *prep.point, scope, now) || !now.Before(p.Removal.ExpiresAt) || scope.Retention.RetainUntil.Before(now.Add(30*24*time.Hour)) {
		return data.QuiescenceFence{}, ErrConflict
	}
	if err = guard.recheckPrepared(ctx, d.Binding.DatabaseID, prep.point.Stopped); err != nil {
		return data.QuiescenceFence{}, err
	}
	claim := sqliteArchiveClaim{PreparationID: ref.PreparationID, ArchiveOperationID: o.ID, TaskOperationID: task.ID, DatabaseID: d.Binding.DatabaseID, FenceID: ref.Fence.ID, OriginalFenceOwner: ref.Fence.OperationID, PreparedSHA256: ref.PreparedSHA256}
	raw, err := json.Marshal(claim)
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_archive_claims VALUES(?,?,?,?,?,?,?,?)", claim.PreparationID, claim.ArchiveOperationID, claim.TaskOperationID, claim.DatabaseID, claim.FenceID, claim.OriginalFenceOwner, claim.PreparedSHA256, raw); err != nil {
		return data.QuiescenceFence{}, err
	}
	raw, err = sqliteprep.Encode(consumer)
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_sqlite_point_consumers VALUES(?,?,?,NULL,?)", consumer.PreparationID, consumer.OperationID, consumer.Kind, raw); err != nil {
		return data.QuiescenceFence{}, err
	}
	return ref.Fence, nil
}

func claimEmptyRemovalDatabase(ctx context.Context, tx *sql.Tx, p archive.Plan, o archive.Operation, d archive.SQLiteDatabase, guard *SQLiteRemovalGuard) (data.QuiescenceFence, error) {
	if d.Written || d.Allocation == nil || !d.Allocation.Valid() {
		return data.QuiescenceFence{}, ErrConflict
	}
	if err := guard.recheckEmpty(ctx, d); err != nil {
		return data.QuiescenceFence{}, err
	}
	id, err := data.NewID()
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	f := data.QuiescenceFence{ID: data.FenceID(id), DatabaseID: d.Binding.DatabaseID, IncarnationID: p.Candidate.Incarnation, OperationID: o.ID, State: data.FenceHeld}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_fences VALUES(?,?,?,?,?)", f.ID, f.DatabaseID, f.IncarnationID, f.OperationID, f.State)
	return f, err
}

func readSQLiteArchiveClaim(ctx context.Context, q dataQuerier, preparation string) (sqliteArchiveClaim, error) {
	var c, columns sqliteArchiveClaim
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT preparation_id,archive_operation_id,task_operation_id,database_id,fence_id,original_fence_owner,prepared_sha256,canonical FROM data_sqlite_archive_claims WHERE preparation_id=?", preparation).Scan(&columns.PreparationID, &columns.ArchiveOperationID, &columns.TaskOperationID, &columns.DatabaseID, &columns.FenceID, &columns.OriginalFenceOwner, &columns.PreparedSHA256, &raw)
	if err != nil {
		return c, err
	}
	if !strictSQLiteRetireObject(raw, "preparation_id", "archive_operation_id", "task_operation_id", "database_id", "fence_id", "original_fence_owner", "prepared_sha256") || json.Unmarshal(raw, &c) != nil || c != columns || c.PreparationID != preparation {
		return c, &IntegrityError{}
	}
	canonical, err := json.Marshal(c)
	if err != nil || string(canonical) != string(raw) {
		return c, &IntegrityError{}
	}
	return c, nil
}

func checkSQLiteArchiveFenceOwner(ctx context.Context, q dataQuerier, archiveOperation string, f data.QuiescenceFence) error {
	var preparation string
	err := q.QueryRowContext(ctx, "SELECT preparation_id FROM data_sqlite_archive_claims WHERE archive_operation_id=? AND database_id=? AND fence_id=? AND original_fence_owner=?", archiveOperation, f.DatabaseID, f.ID, f.OperationID).Scan(&preparation)
	if err != nil {
		return err
	}
	prep, err := readSQLitePreparation(ctx, q, preparation)
	if err != nil || prep.intent.Fence.ID != f.ID || prep.intent.Fence.OperationID != f.OperationID || prep.intent.Scope.DatabaseID != f.DatabaseID || prep.intent.Scope.IncarnationID != f.IncarnationID || !prep.consumed {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func checkRemovalAuthority(p archive.Plan, g *SQLiteRemovalGuard, now time.Time) error {
	authority := g.authority.Load()
	if authority == nil || authority.target != p.TargetHash || authority.policy.Hash() != p.PolicyHash {
		return ErrConflict
	}
	for _, variant := range p.Candidate.Databases {
		d, ok := variant.SQLite()
		if !ok || len(d.Epochs) != 1 || !slices.Contains(authority.policy.PersistentRoots(), string(d.Binding.Root)) {
			return ErrConflict
		}
		dest, ok := authority.policy.BackupDestination(d.Epochs[0].Destination.Reference)
		if !ok || dest != d.Epochs[0].Destination {
			return ErrConflict
		}
		evidence, ok := authority.policy.BackupRetention(dest.Reference)
		if !ok || len(d.Retention) != 1 || evidence != d.Retention[0] || !evidence.Admits(dest, now) {
			return ErrConflict
		}
	}
	return nil
}
