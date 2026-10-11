package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
)

func migrateArchiveDispositions(ctx context.Context, tx *sql.Tx) error {
	// Rebuild only the archive operation family to replace lifetime-exclusive
	// incarnation ownership with held-fence exclusivity. Abandonment permits a
	// later independent archive attempt without rewriting any retained evidence.
	_, err := tx.ExecContext(ctx, `
 CREATE TABLE data_archive_operations_v13(id TEXT PRIMARY KEY,plan_id TEXT NOT NULL UNIQUE REFERENCES data_archive_plans(id),incarnation_id TEXT NOT NULL REFERENCES data_incarnations(id));
 CREATE TABLE data_archive_fences_v13(operation_id TEXT NOT NULL REFERENCES data_archive_operations_v13(id),database_id TEXT NOT NULL REFERENCES data_databases(id),fence_id TEXT NOT NULL UNIQUE REFERENCES data_fences(id),PRIMARY KEY(operation_id,database_id));
 CREATE TABLE data_archive_events_v13(operation_id TEXT NOT NULL REFERENCES data_archive_operations_v13(id),sequence INTEGER NOT NULL CHECK(sequence>0),stage TEXT NOT NULL,created_at TEXT NOT NULL,coverage BLOB CHECK(coverage IS NULL OR length(coverage)<=1048576),PRIMARY KEY(operation_id,sequence));
 CREATE TABLE data_archive_manifests_v13(archive_id TEXT PRIMARY KEY,operation_id TEXT NOT NULL UNIQUE REFERENCES data_archive_operations_v13(id),canonical BLOB NOT NULL CHECK(length(canonical)<=1048576));
 INSERT INTO data_archive_operations_v13 SELECT * FROM data_archive_operations;
 INSERT INTO data_archive_fences_v13 SELECT * FROM data_archive_fences;
 INSERT INTO data_archive_events_v13 SELECT * FROM data_archive_events;
 INSERT INTO data_archive_manifests_v13 SELECT * FROM data_archive_manifests;
 DROP TABLE data_archive_manifests; DROP TABLE data_archive_events; DROP TABLE data_archive_fences; DROP TABLE data_archive_operations;
 ALTER TABLE data_archive_operations_v13 RENAME TO data_archive_operations;
 ALTER TABLE data_archive_fences_v13 RENAME TO data_archive_fences;
 ALTER TABLE data_archive_events_v13 RENAME TO data_archive_events;
 ALTER TABLE data_archive_manifests_v13 RENAME TO data_archive_manifests;
 CREATE TABLE data_archive_dispositions(archive_id TEXT NOT NULL REFERENCES data_archive_plans(archive_id),sequence INTEGER NOT NULL CHECK(sequence>0),canonical BLOB NOT NULL CHECK(length(canonical)<=4096),PRIMARY KEY(archive_id,sequence));
 CREATE TABLE data_archive_retirements(archive_id TEXT PRIMARY KEY REFERENCES data_archive_plans(archive_id),incarnation_id TEXT NOT NULL UNIQUE REFERENCES data_incarnations(id),app TEXT NOT NULL,release_id TEXT NOT NULL,canonical BLOB NOT NULL CHECK(length(canonical)<=4096));
 UPDATE schema_version SET version=13;
 `)
	if err != nil {
		return err
	}
	for _, table := range []string{"data_archive_operations", "data_archive_fences", "data_archive_events", "data_archive_manifests", "data_archive_dispositions", "data_archive_retirements"} {
		for _, action := range []string{"UPDATE", "DELETE"} {
			if _, err = tx.ExecContext(ctx, "CREATE TRIGGER "+table+"_no_"+action+" BEFORE "+action+" ON "+table+" BEGIN SELECT RAISE(ABORT,'immutable archive history'); END;"); err != nil {
				return err
			}
		}
	}
	return nil
}
func readArchiveDisposition(ctx context.Context, q dataQuerier, id data.ArchiveID) (out archive.Disposition, exists bool, err error) {
	p, err := loadArchivePlanByArchive(ctx, q, id)
	if err != nil {
		return out, false, err
	}
	rows, err := q.QueryContext(ctx, "SELECT sequence,canonical FROM data_archive_dispositions WHERE archive_id=? ORDER BY sequence", id)
	if err != nil {
		return out, false, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var n uint64
		var raw []byte
		var next archive.Disposition
		if err = rows.Scan(&n, &raw); err != nil {
			return out, false, err
		}
		if len(raw) > 4096 || json.Unmarshal(raw, &next) != nil || n != next.Sequence || next.ArchiveID != id || !archive.DispositionNext(out, next, len(p.Trees)) {
			return out, false, &IntegrityError{}
		}
		out = next
		exists = true
	}
	return out, exists, rows.Err()
}
func loadArchivePlanByArchive(ctx context.Context, q dataQuerier, id data.ArchiveID) (archive.Plan, error) {
	var planID string
	if !data.ValidID(string(id)) {
		return archive.Plan{}, ErrInvalid
	}
	if err := q.QueryRowContext(ctx, "SELECT id FROM data_archive_plans WHERE archive_id=?", id).Scan(&planID); err != nil {
		return archive.Plan{}, err
	}
	return loadArchivePlan(ctx, q, planID)
}

// LoadArchivePlanByArchive resolves the immutable identity even before a manifest exists.
func (s *Store) LoadArchivePlanByArchive(ctx context.Context, id data.ArchiveID) (archive.Plan, error) {
	return loadArchivePlanByArchive(ctx, s.db, id)
}
func (s *Store) ReadArchiveDisposition(ctx context.Context, id data.ArchiveID) (archive.Disposition, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return archive.Disposition{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	d, exists, err := readArchiveDisposition(ctx, tx, id)
	if err != nil {
		return d, exists, err
	}
	return d, exists, tx.Commit()
}
func validateDispositionOperation(ctx context.Context, tx *sql.Tx, o archive.Operation, old, next archive.Disposition) (archive.Plan, error) {
	p, err := loadArchivePlan(ctx, tx, o.PlanID)
	if err != nil {
		return p, err
	}
	current, exists, err := readArchiveOperation(ctx, tx, o.PlanID)
	if err != nil || !exists || !reflect.DeepEqual(current, o) || p.ArchiveID != next.ArchiveID {
		return p, ErrConflict
	}
	state, _, err := readArchiveDisposition(ctx, tx, p.ArchiveID)
	if err != nil || !reflect.DeepEqual(state, old) || !archive.DispositionNext(old, next, len(p.Trees)) {
		return p, ErrConflict
	}
	if next.Stage != archive.AbandonIntent && next.Stage != archive.Abandoned {
		var manifested string
		if current.Stage != archive.Prepared {
			return p, ErrConflict
		}
		if err = tx.QueryRowContext(ctx, "SELECT operation_id FROM data_archive_manifests WHERE archive_id=?", p.ArchiveID).Scan(&manifested); err != nil || manifested != o.ID {
			return p, ErrConflict
		}
	}
	// Active identity must still be this incarnation, even while its source paths
	// have moved. Never let a delayed receipt retire a recreated app.
	var active string
	if err = tx.QueryRowContext(ctx, "SELECT incarnation_id FROM data_active_incarnations WHERE app=?", p.Candidate.App).Scan(&active); err != nil || active != string(p.Candidate.Incarnation) {
		return p, ErrConflict
	}
	var release string
	err = tx.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", p.Candidate.App).Scan(&release)
	if p.Candidate.ReleaseID == "" {
		if !errors.Is(err, sql.ErrNoRows) {
			return p, ErrConflict
		}
	} else if err != nil || release != p.Candidate.ReleaseID {
		return p, ErrConflict
	}
	return p, nil
}
func insertDisposition(ctx context.Context, tx *sql.Tx, next archive.Disposition) error {
	raw, err := json.Marshal(next)
	if err != nil || len(raw) > 4096 {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_archive_dispositions VALUES(?,?,?)", next.ArchiveID, next.Sequence, raw)
	return err
}
func (s *Store) AdvanceArchiveDisposition(ctx context.Context, o archive.Operation, old, next archive.Disposition) (archive.Disposition, error) {
	if next.Stage == archive.Retired || next.Stage == archive.Abandoned {
		return old, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return old, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = validateDispositionOperation(ctx, tx, o, old, next); err != nil {
		return old, err
	}
	if err = insertDisposition(ctx, tx, next); err != nil {
		return old, err
	}
	return next, tx.Commit()
}
func (s *Store) FinishArchiveDisposition(ctx context.Context, o archive.Operation, old, next archive.Disposition) (archive.Disposition, error) {
	if next.Stage != archive.Retired && next.Stage != archive.Abandoned || next.CommittedAt.After(time.Now().UTC()) || time.Since(next.CommittedAt) > archive.MaxEvidenceAge {
		return old, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return old, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := validateDispositionOperation(ctx, tx, o, old, next)
	if err != nil {
		return old, err
	}
	if next.Stage == archive.Abandoned {
		candidate, err := readArchiveCandidateOwned(ctx, tx, p.Candidate.App, o.ID)
		if err != nil {
			return old, err
		}
		if len(candidate.Databases) != len(p.Candidate.Databases) {
			return old, ErrConflict
		}
		for i := range candidate.Databases {
			candidate.Databases[i].Retention = p.Candidate.Databases[i].Retention
		}
		if !reflect.DeepEqual(candidate, p.Candidate) {
			return old, ErrConflict
		}
		for _, f := range o.Fences {
			result, err := tx.ExecContext(ctx, "UPDATE data_fences SET state='released' WHERE id=? AND state='held' AND operation_id=?", f.ID, o.ID)
			if err != nil {
				return old, err
			}
			n, err := result.RowsAffected()
			if err != nil || n != 1 {
				return old, ErrConflict
			}
		}
	} else {
		result, err := tx.ExecContext(ctx, "DELETE FROM data_active_incarnations WHERE app=? AND incarnation_id=?", p.Candidate.App, p.Candidate.Incarnation)
		if err != nil {
			return old, err
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return old, ErrConflict
		}
		if p.Candidate.ReleaseID != "" {
			result, err = tx.ExecContext(ctx, "DELETE FROM release_heads WHERE app=? AND current_id=?", p.Candidate.App, p.Candidate.ReleaseID)
			if err != nil {
				return old, err
			}
			n, err = result.RowsAffected()
			if err != nil || n != 1 {
				return old, ErrConflict
			}
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return old, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO data_archive_retirements VALUES(?,?,?,?,?)", p.ArchiveID, p.Candidate.Incarnation, p.Candidate.App, p.Candidate.ReleaseID, raw); err != nil {
			return old, err
		}
	}
	if err = insertDisposition(ctx, tx, next); err != nil {
		return old, err
	}
	return next, tx.Commit()
}

// ArchiveOwnedCandidate admits only held fences belonging to this exact archive
// operation. It is inspection evidence, never a writer or replica startup permit.
func (s *Store) ArchiveOwnedCandidate(ctx context.Context, app, operation string) (archive.Candidate, error) {
	if !data.ValidID(operation) {
		return archive.Candidate{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return archive.Candidate{}, err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := readArchiveCandidateOwned(ctx, tx, app, operation)
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}
