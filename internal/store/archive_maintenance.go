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

func migrateArchiveMaintenance(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE data_archive_maintenance_plans(id TEXT PRIMARY KEY,archive_id TEXT NOT NULL REFERENCES data_archive_retirements(archive_id),canonical BLOB NOT NULL CHECK(length(canonical)<=16384));
CREATE TABLE data_archive_deletions(archive_id TEXT NOT NULL REFERENCES data_archive_retirements(archive_id),sequence INTEGER NOT NULL CHECK(sequence>0),plan_id TEXT NOT NULL REFERENCES data_archive_maintenance_plans(id),canonical BLOB NOT NULL CHECK(length(canonical)<=4096),PRIMARY KEY(archive_id,sequence));
CREATE TABLE data_archive_retention_proofs(archive_id TEXT NOT NULL REFERENCES data_archive_retirements(archive_id),sequence INTEGER NOT NULL CHECK(sequence>0),canonical BLOB NOT NULL CHECK(length(canonical)<=1048576),PRIMARY KEY(archive_id,sequence));
CREATE TABLE data_archive_credential_plans(id TEXT PRIMARY KEY,archive_id TEXT NOT NULL REFERENCES data_archive_retirements(archive_id),canonical BLOB NOT NULL CHECK(length(canonical)<=8192));
CREATE TABLE data_archive_credential_receipts(plan_id TEXT PRIMARY KEY REFERENCES data_archive_credential_plans(id),archive_id TEXT NOT NULL REFERENCES data_archive_retirements(archive_id),binding_id TEXT NOT NULL,epoch_id TEXT NOT NULL,version INTEGER NOT NULL CHECK(version>0),canonical BLOB NOT NULL CHECK(length(canonical)<=8192),UNIQUE(archive_id,binding_id,epoch_id,version));
UPDATE schema_version SET version=14;`)
	if err != nil {
		return err
	}
	for _, table := range []string{"data_archive_maintenance_plans", "data_archive_deletions", "data_archive_retention_proofs", "data_archive_credential_plans", "data_archive_credential_receipts"} {
		for _, action := range []string{"UPDATE", "DELETE"} {
			if _, err = tx.ExecContext(ctx, "CREATE TRIGGER "+table+"_no_"+action+" BEFORE "+action+" ON "+table+" BEGIN SELECT RAISE(ABORT,'immutable archive maintenance history'); END;"); err != nil {
				return err
			}
		}
	}
	return nil
}

func retiredArchive(ctx context.Context, q dataQuerier, id data.ArchiveID) (archive.Plan, archive.Disposition, error) {
	p, err := loadArchivePlanByArchive(ctx, q, id)
	if err != nil {
		return p, archive.Disposition{}, err
	}
	d, exists, err := readArchiveDisposition(ctx, q, id)
	if err != nil || !exists || d.Stage != archive.Retired || !d.RetainUntil.Equal(d.CommittedAt.Add(30*24*time.Hour)) {
		return p, d, ErrConflict
	}
	return p, d, nil
}

func loadMaintenancePlan(ctx context.Context, q dataQuerier, id string) (out archive.MaintenancePlan, err error) {
	if !digestPattern.MatchString(id) {
		return out, ErrInvalid
	}
	var raw []byte
	var archiveID string
	if err = q.QueryRowContext(ctx, "SELECT archive_id,canonical FROM data_archive_maintenance_plans WHERE id=?", id).Scan(&archiveID, &raw); err != nil {
		return out, err
	}
	if len(raw) > 16384 || json.Unmarshal(raw, &out) != nil || !out.Valid() || out.ID != id || string(out.ArchiveID) != archiveID {
		return out, &IntegrityError{}
	}
	canonical, err := json.Marshal(out)
	if err != nil || !bytes.Equal(raw, canonical) {
		return out, &IntegrityError{}
	}
	p, d, err := retiredArchive(ctx, q, out.ArchiveID)
	if err != nil || out.ArchivePlanID != p.ID || out.Requester != p.Requester || !out.RetiredAt.Equal(d.CommittedAt) || !out.RetainUntil.Equal(d.RetainUntil) || len(out.Parents) != len(p.Trees) {
		return out, &IntegrityError{}
	}
	for i, parent := range out.Parents {
		if !parent.Valid(p.Trees[i]) {
			return out, &IntegrityError{}
		}
	}
	return out, nil
}

func (s *Store) LoadMaintenancePlan(ctx context.Context, id string) (archive.MaintenancePlan, error) {
	return loadMaintenancePlan(ctx, s.db, id)
}
func (s *Store) SaveMaintenancePlan(ctx context.Context, p archive.MaintenancePlan) error {
	if !p.Valid() {
		return ErrInvalid
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 16384 {
		return ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Insert and re-read within the same transaction: the loader validates the
	// original retirement, immutable archive plan, parent identities and deadline.
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_archive_maintenance_plans VALUES(?,?,?) ON CONFLICT(id) DO NOTHING", p.ID, p.ArchiveID, raw); err != nil {
		return err
	}
	stored, err := loadMaintenancePlan(ctx, tx, p.ID)
	if err != nil || !reflect.DeepEqual(stored, p) {
		return ErrConflict
	}
	deletion, err := readArchiveDeletion(ctx, tx, p.ArchiveID)
	if err != nil || deletion.Sequence != 0 {
		return ErrConflict
	}
	return tx.Commit()
}

func readArchiveDeletion(ctx context.Context, q dataQuerier, id data.ArchiveID) (out archive.Deletion, err error) {
	p, _, err := retiredArchive(ctx, q, id)
	if err != nil {
		return out, err
	}
	rows, err := q.QueryContext(ctx, "SELECT sequence,plan_id,canonical FROM data_archive_deletions WHERE archive_id=? ORDER BY sequence", id)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var n uint64
		var planID string
		var raw []byte
		var next archive.Deletion
		if err = rows.Scan(&n, &planID, &raw); err != nil {
			return out, err
		}
		if len(raw) > 4096 || json.Unmarshal(raw, &next) != nil || next.Sequence != n || next.ArchiveID != id || next.PlanID != planID || !archive.DeletionNext(out, next, len(p.Trees)) {
			return out, &IntegrityError{}
		}
		canonical, marshalErr := json.Marshal(next)
		if marshalErr != nil || !bytes.Equal(raw, canonical) {
			return out, &IntegrityError{}
		}
		out = next
	}
	return out, rows.Err()
}
func (s *Store) ReadArchiveDeletion(ctx context.Context, id data.ArchiveID) (archive.Deletion, error) {
	return readArchiveDeletion(ctx, s.db, id)
}
func (s *Store) AdvanceArchiveDeletion(ctx context.Context, old, next archive.Deletion) error {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	p, d, err := retiredArchive(ctx, tx, next.ArchiveID)
	if err != nil || !archive.DeletionNext(old, next, len(p.Trees)) {
		return ErrConflict
	}
	reviewed, err := loadMaintenancePlan(ctx, tx, next.PlanID)
	if err != nil || reviewed.ArchiveID != next.ArchiveID || reviewed.Kind != next.Kind || next.StartedAt.Before(reviewed.CreatedAt) || next.StartedAt.Before(d.CommittedAt) || next.Kind == archive.ExpireArchive && next.StartedAt.Before(d.RetainUntil) {
		return ErrConflict
	}
	current, err := readArchiveDeletion(ctx, tx, next.ArchiveID)
	if err != nil || !reflect.DeepEqual(current, old) {
		return ErrConflict
	}
	raw, err := json.Marshal(next)
	if err != nil || len(raw) > 4096 {
		return ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_archive_deletions VALUES(?,?,?,?)", next.ArchiveID, next.Sequence, next.PlanID, raw); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveRetentionProof(ctx context.Context, proof archive.RetentionProof) error {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	p, d, err := retiredArchive(ctx, tx, proof.ArchiveID)
	if err != nil {
		return err
	}
	manifest, err := readArchiveManifest(ctx, tx, proof.ArchiveID)
	if err != nil || !archive.RetentionProofMatches(proof, p, d, manifest, time.Now().UTC()) {
		return ErrInvalid
	}
	deletion, err := readArchiveDeletion(ctx, tx, proof.ArchiveID)
	if err != nil || deletion.Sequence != 0 {
		return ErrConflict
	}
	raw, err := json.Marshal(proof)
	if err != nil || len(raw) > 1048576 {
		return ErrInvalid
	}
	var sequence uint64
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(sequence),0)+1 FROM data_archive_retention_proofs WHERE archive_id=?", proof.ArchiveID).Scan(&sequence); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_archive_retention_proofs VALUES(?,?,?)", proof.ArchiveID, sequence, raw); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ReadRetentionProof(ctx context.Context, id data.ArchiveID) (out archive.RetentionProof, err error) {
	p, d, err := retiredArchive(ctx, s.db, id)
	if err != nil {
		return out, err
	}
	var raw []byte
	if err = s.db.QueryRowContext(ctx, "SELECT canonical FROM data_archive_retention_proofs WHERE archive_id=? ORDER BY sequence DESC LIMIT 1", id).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
		return out, nil
	} else if err != nil {
		return out, err
	}
	manifest, err := readArchiveManifest(ctx, s.db, id)
	if err != nil || len(raw) > 1048576 || json.Unmarshal(raw, &out) != nil || !archive.RetentionProofMatches(out, p, d, manifest, out.VerifiedAt) {
		return out, &IntegrityError{}
	}
	canonical, err := json.Marshal(out)
	if err != nil || !bytes.Equal(raw, canonical) {
		return out, &IntegrityError{}
	}
	return out, nil
}

var _ archive.MaintenanceJournal = (*Store)(nil)
