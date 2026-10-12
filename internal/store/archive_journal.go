package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
)

func migrateArchives(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
 CREATE TABLE data_archive_plans(id TEXT PRIMARY KEY,archive_id TEXT NOT NULL UNIQUE,canonical BLOB NOT NULL CHECK(length(canonical)<=1048576));
 CREATE TABLE data_archive_operations(id TEXT PRIMARY KEY,plan_id TEXT NOT NULL UNIQUE REFERENCES data_archive_plans(id),incarnation_id TEXT NOT NULL UNIQUE REFERENCES data_incarnations(id));
 CREATE TABLE data_archive_fences(operation_id TEXT NOT NULL REFERENCES data_archive_operations(id),database_id TEXT NOT NULL REFERENCES data_databases(id),fence_id TEXT NOT NULL UNIQUE REFERENCES data_fences(id),PRIMARY KEY(operation_id,database_id));
 CREATE TABLE data_archive_events(operation_id TEXT NOT NULL REFERENCES data_archive_operations(id),sequence INTEGER NOT NULL CHECK(sequence>0),stage TEXT NOT NULL,created_at TEXT NOT NULL,coverage BLOB CHECK(coverage IS NULL OR length(coverage)<=1048576),PRIMARY KEY(operation_id,sequence));
 CREATE TABLE data_archive_manifests(archive_id TEXT PRIMARY KEY,operation_id TEXT NOT NULL UNIQUE REFERENCES data_archive_operations(id),canonical BLOB NOT NULL CHECK(length(canonical)<=1048576));
 UPDATE schema_version SET version=12;
 `)
	if err != nil {
		return err
	}
	for _, table := range []string{"data_archive_plans", "data_archive_operations", "data_archive_fences", "data_archive_events", "data_archive_manifests"} {
		for _, action := range []string{"UPDATE", "DELETE"} {
			if _, err = tx.ExecContext(ctx, "CREATE TRIGGER "+table+"_no_"+action+" BEFORE "+action+" ON "+table+" BEGIN SELECT RAISE(ABORT,'immutable archive history'); END;"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) SaveArchivePlan(ctx context.Context, p archive.Plan) error {
	if !p.Valid() {
		return ErrInvalid
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > archive.MaxManifestBytes {
		return ErrInvalid
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO data_archive_plans VALUES(?,?,?) ON CONFLICT(id) DO NOTHING", p.ID, p.ArchiveID, raw)
	return err
}
func loadArchivePlan(ctx context.Context, q dataQuerier, id string) (archive.Plan, error) {
	var p archive.Plan
	var raw []byte
	var archiveID string
	if err := q.QueryRowContext(ctx, "SELECT archive_id,canonical FROM data_archive_plans WHERE id=?", id).Scan(&archiveID, &raw); err != nil {
		return p, err
	}
	if len(raw) > archive.MaxManifestBytes || json.Unmarshal(raw, &p) != nil || !p.Valid() || p.ID != id || string(p.ArchiveID) != archiveID {
		return archive.Plan{}, &IntegrityError{}
	}
	encoded, err := json.Marshal(p)
	if err != nil || !bytes.Equal(raw, encoded) {
		return archive.Plan{}, &IntegrityError{}
	}
	return p, nil
}
func (s *Store) LoadArchivePlan(ctx context.Context, id string) (archive.Plan, error) {
	return loadArchivePlan(ctx, s.db, id)
}

func readArchiveOperation(ctx context.Context, q dataQuerier, id string) (o archive.Operation, exists bool, err error) {
	var sequence int64
	err = q.QueryRowContext(ctx, `SELECT o.id,p.id,p.archive_id,e.stage,e.sequence FROM data_archive_operations o JOIN data_archive_plans p ON p.id=o.plan_id JOIN data_archive_events e ON e.operation_id=o.id WHERE o.plan_id=? ORDER BY e.sequence DESC LIMIT 1`, id).Scan(&o.ID, &o.PlanID, &o.ArchiveID, &o.Stage, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, nil
	}
	if err != nil {
		return o, false, err
	}
	if sequence <= 0 || !data.ValidID(o.ID) {
		return o, false, &IntegrityError{}
	}
	o.Sequence = uint64(sequence)
	stage, ok := archive.StageAt(o.Sequence)
	if !ok || stage != o.Stage {
		return o, false, &IntegrityError{}
	}
	chain, err := q.QueryContext(ctx, "SELECT sequence,stage FROM data_archive_events WHERE operation_id=? ORDER BY sequence", o.ID)
	if err != nil {
		return o, false, err
	}
	var count uint64
	for chain.Next() {
		var n uint64
		var stage archive.Stage
		if err = chain.Scan(&n, &stage); err != nil {
			return o, false, errors.Join(err, chain.Close())
		}
		count++
		expected, ok := archive.StageAt(count)
		if n != count || !ok || stage != expected {
			return o, false, errors.Join(&IntegrityError{}, chain.Close())
		}
	}
	if err = errors.Join(chain.Err(), chain.Close()); err != nil {
		return o, false, err
	}
	if count != o.Sequence {
		return o, false, &IntegrityError{}
	}
	requiredFence := data.FenceHeld
	disposition, _, err := readArchiveDisposition(ctx, q, o.ArchiveID)
	if err != nil {
		return o, false, err
	}
	if disposition.Stage == archive.Abandoned {
		requiredFence = data.FenceReleased
	}
	o.Fences = []data.QuiescenceFence{}
	rows, err := q.QueryContext(ctx, `SELECT f.id,f.incarnation_id,f.database_id,f.operation_id,f.state FROM data_archive_fences a JOIN data_fences f ON f.id=a.fence_id WHERE a.operation_id=? ORDER BY a.database_id`, o.ID)
	if err != nil {
		return o, false, err
	}
	for rows.Next() {
		var fence data.QuiescenceFence
		if err = rows.Scan(&fence.ID, &fence.IncarnationID, &fence.DatabaseID, &fence.OperationID, &fence.State); err != nil {
			return o, false, errors.Join(err, rows.Close())
		}
		if fence.OperationID != o.ID || fence.State != requiredFence {
			return o, false, errors.Join(&IntegrityError{}, rows.Close())
		}
		o.Fences = append(o.Fences, fence)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if len(o.Fences) == 0 {
		return o, false, &IntegrityError{}
	}
	return o, err == nil, err
}
func (s *Store) ReadArchiveOperation(ctx context.Context, id string) (archive.Operation, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return archive.Operation{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	o, exists, err := readArchiveOperation(ctx, tx, id)
	if err != nil {
		return o, exists, err
	}
	return o, exists, tx.Commit()
}
func (s *Store) ClaimArchive(ctx context.Context, p archive.Plan) (archive.Operation, error) {
	o := archive.Operation{PlanID: p.ID, ArchiveID: p.ArchiveID, Stage: archive.Intent, Sequence: 1, Fences: []data.QuiescenceFence{}}
	if !p.Valid() {
		return o, ErrInvalid
	}
	databases := make([]data.DatabaseID, len(p.Candidate.Databases))
	for i, database := range p.Candidate.Databases {
		databases[i] = database.Binding.DatabaseID
	}
	guards, err := s.guardEngineDatabases(ctx, databases)
	if err != nil {
		return o, err
	}
	defer func() { _ = guards.Release() }()
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return o, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, err := loadArchivePlan(ctx, tx, p.ID)
	if err != nil || !reflect.DeepEqual(retained, p) {
		return o, ErrConflict
	}
	candidate, err := readArchiveCandidate(ctx, tx, p.Candidate.App)
	if err != nil {
		return o, err
	}
	if len(candidate.Databases) != len(p.Candidate.Databases) {
		return o, ErrConflict
	}
	for i := range candidate.Databases {
		candidate.Databases[i].Retention = p.Candidate.Databases[i].Retention
	}
	if !reflect.DeepEqual(candidate, p.Candidate) {
		return o, ErrConflict
	}
	o.ID, err = data.NewID()
	if err != nil {
		return o, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_archive_operations VALUES(?,?,?)", o.ID, p.ID, p.Candidate.Incarnation); err != nil {
		return o, err
	}
	for _, database := range p.Candidate.Databases {
		id, err := data.NewID()
		if err != nil {
			return o, err
		}
		fence := data.QuiescenceFence{ID: data.FenceID(id), IncarnationID: p.Candidate.Incarnation, DatabaseID: database.Binding.DatabaseID, OperationID: o.ID, State: data.FenceHeld}
		if _, err = tx.ExecContext(ctx, "INSERT INTO data_fences VALUES(?,?,?,?,?)", fence.ID, fence.DatabaseID, fence.IncarnationID, fence.OperationID, fence.State); err != nil {
			return o, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO data_archive_fences VALUES(?,?,?)", o.ID, fence.DatabaseID, fence.ID); err != nil {
			return o, err
		}
		o.Fences = append(o.Fences, fence)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_archive_events VALUES(?,?,?, ?,NULL)", o.ID, o.Sequence, o.Stage, timestamp()); err != nil {
		return o, err
	}
	return o, tx.Commit()
}

func appendArchiveEvent(ctx context.Context, tx *sql.Tx, o archive.Operation, next archive.Stage, coverage []byte) (archive.Operation, error) {
	current, exists, err := readArchiveOperation(ctx, tx, o.PlanID)
	if err != nil || !exists || !reflect.DeepEqual(current, o) || !archive.Next(o.Stage, next) {
		return o, ErrConflict
	}
	o.Sequence++
	o.Stage = next
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_archive_events VALUES(?,?,?,?,?)", o.ID, o.Sequence, o.Stage, timestamp(), coverage); err != nil {
		return o, err
	}
	return o, nil
}
func (s *Store) AdvanceArchive(ctx context.Context, o archive.Operation, next archive.Stage) (archive.Operation, error) {
	if next == archive.CoverageVerified || next == archive.Prepared {
		return o, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return o, err
	}
	defer func() { _ = tx.Rollback() }()
	updated, err := appendArchiveEvent(ctx, tx, o, next, nil)
	if err != nil {
		return o, err
	}
	return updated, tx.Commit()
}
func (s *Store) SaveArchiveCoverage(ctx context.Context, o archive.Operation, coverage []archive.Coverage) (archive.Operation, error) {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return o, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := loadArchivePlan(ctx, tx, o.PlanID)
	if err != nil || len(coverage) != len(p.Candidate.Databases) {
		return o, ErrInvalid
	}
	for i, point := range coverage {
		if !point.Admits(p, p.Candidate.Databases[i], o.ID, time.Now().UTC()) {
			return o, ErrInvalid
		}
		if point.Point != nil {
			raw, err := json.Marshal(point.Point)
			if err != nil {
				return o, err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO data_restore_points VALUES(?,?,?,?)", point.Point.ID, point.Point.BindingID, point.Point.EpochID, raw); err != nil {
				return o, err
			}
		}
	}
	raw, err := json.Marshal(coverage)
	if err != nil || len(raw) > archive.MaxManifestBytes {
		return o, ErrInvalid
	}
	updated, err := appendArchiveEvent(ctx, tx, o, archive.CoverageVerified, raw)
	if err != nil {
		return o, err
	}
	return updated, tx.Commit()
}
func (s *Store) SaveArchiveManifest(ctx context.Context, o archive.Operation, m archive.Manifest) (archive.Operation, error) {
	if !m.Valid() || m.OperationID != o.ID || m.Plan.ID != o.PlanID || m.Plan.ArchiveID != o.ArchiveID || time.Now().UTC().Before(m.PreparedAt) || time.Since(m.PreparedAt) > archive.MaxEvidenceAge {
		return o, ErrInvalid
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw) > archive.MaxManifestBytes {
		return o, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return o, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := loadArchivePlan(ctx, tx, o.PlanID)
	if err != nil || !reflect.DeepEqual(p, m.Plan) {
		return o, ErrConflict
	}
	var evidence []byte
	if err = tx.QueryRowContext(ctx, "SELECT coverage FROM data_archive_events WHERE operation_id=? AND stage=?", o.ID, archive.CoverageVerified).Scan(&evidence); err != nil {
		return o, err
	}
	expected, err := json.Marshal(m.Coverage)
	if err != nil || !bytes.Equal(expected, evidence) {
		return o, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_archive_manifests VALUES(?,?,?)", o.ArchiveID, o.ID, raw); err != nil {
		return o, err
	}
	updated, err := appendArchiveEvent(ctx, tx, o, archive.Prepared, nil)
	if err != nil {
		return o, err
	}
	return updated, tx.Commit()
}
func (s *Store) ReadArchiveManifest(ctx context.Context, id data.ArchiveID) (archive.Manifest, error) {
	return readArchiveManifest(ctx, s.db, id)
}
func readArchiveManifest(ctx context.Context, q dataQuerier, id data.ArchiveID) (archive.Manifest, error) {
	var m archive.Manifest
	var raw []byte
	if !data.ValidID(string(id)) {
		return m, ErrInvalid
	}
	if err := q.QueryRowContext(ctx, "SELECT canonical FROM data_archive_manifests WHERE archive_id=?", id).Scan(&raw); err != nil {
		return m, err
	}
	if len(raw) > archive.MaxManifestBytes || json.Unmarshal(raw, &m) != nil || !m.Valid() || m.Plan.ArchiveID != id {
		return archive.Manifest{}, &IntegrityError{}
	}
	encoded, err := json.Marshal(m)
	if err != nil || !bytes.Equal(raw, encoded) {
		return archive.Manifest{}, &IntegrityError{}
	}
	return m, nil
}
