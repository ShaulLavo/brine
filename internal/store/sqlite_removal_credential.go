package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
)

// ReadSQLiteRemovalCredential returns the ref's exact protected delivery, not a
// current-version selection or permission to use its private packet.
func (s *Store) ReadSQLiteRemovalCredential(ctx context.Context, planID string, database data.DatabaseID) (CredentialRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CredentialRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := loadArchivePlan(ctx, tx, planID)
	if err != nil || p.Removal == nil {
		return CredentialRecord{}, errors.Join(ErrConflict, err)
	}
	ref, found := removalPlanRef(p, database)
	if !found || ref.CredentialVersion == 0 {
		return CredentialRecord{}, ErrNotFound
	}
	prep, err := readSQLitePreparation(ctx, tx, ref.PreparationID)
	if err != nil || prep.state != ops.Succeeded || prep.intent.Scope.App != p.Candidate.App || prep.intent.Scope.IncarnationID != p.Candidate.Incarnation || prep.intent.Scope.DatabaseID != database {
		return CredentialRecord{}, errors.Join(ErrConflict, err)
	}
	expected, err := removalRef(prep)
	if err != nil || expected != ref {
		return CredentialRecord{}, errors.Join(ErrConflict, err)
	}
	if err := checkRemovalCredentialClaim(ctx, tx, p, prep); err != nil {
		return CredentialRecord{}, err
	}
	replica, err := readSQLiteRetainedReplica(ctx, tx, prep.intent.Scope)
	if err != nil {
		return CredentialRecord{}, err
	}
	record, err := readSQLiteCredential(ctx, tx, p.Candidate.App, replica, ref.CredentialVersion)
	if err != nil {
		return CredentialRecord{}, err
	}
	return record, tx.Commit()
}

func checkRemovalCredentialClaim(ctx context.Context, tx *sql.Tx, p archive.Plan, prep sqlitePreparation) error {
	if !prep.consumed {
		return nil
	}
	claim, err := readSQLiteArchiveClaim(ctx, tx, prep.intent.Input.PreparationID)
	if err != nil {
		return err
	}
	var planID string
	err = tx.QueryRowContext(ctx, "SELECT plan_id FROM data_archive_operations WHERE id=?", claim.ArchiveOperationID).Scan(&planID)
	if err != nil || planID != p.ID {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
