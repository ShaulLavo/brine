package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
)

func readPublicationClaimTx(ctx context.Context, tx *sql.Tx, planID string) (publicationClaim, error) {
	var claim publicationClaim
	var raw []byte
	claim.PlanID = planID
	err := tx.QueryRowContext(ctx, "SELECT point_id,apply_operation_id,requester,binding_id,old_epoch_id,new_epoch_id,consumer_sha256,repository_intent_id,repository_intent_sha256,canonical FROM data_module_publication_claims WHERE plan_id=?", planID).Scan(&claim.PointID, &claim.ApplyOpID, &claim.Requester, &claim.BindingID, &claim.OldEpochID, &claim.NewEpochID, &claim.ConsumerSHA, &claim.RepositoryIntentID, &claim.RepositoryIntentSHA, &raw)
	if err != nil {
		return claim, err
	}
	canonical, err := json.Marshal(claim)
	if err != nil || !bytes.Equal(raw, canonical) || !data.ValidID(claim.PointID) || !data.ValidID(string(claim.BindingID)) || !data.ValidID(string(claim.OldEpochID)) || !data.ValidID(string(claim.NewEpochID)) || claim.OldEpochID == claim.NewEpochID || !data.ValidCatalogHash(claim.ConsumerSHA) || !data.ValidID(claim.RepositoryIntentID) || !data.ValidCatalogHash(claim.RepositoryIntentSHA) {
		return publicationClaim{}, &IntegrityError{}
	}
	consumer, err := readNativePointConsumer(ctx, tx, claim.PointID)
	if err != nil || publicationStorageSHA256(consumer) != claim.ConsumerSHA {
		return publicationClaim{}, &IntegrityError{}
	}
	var exact int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_native_point_consumers WHERE point_id=? AND kind=? AND plan_id=? AND apply_operation_id=? AND requester=? AND binding_id=? AND epoch_id=?", claim.PointID, nativePointModulePublish, planID, claim.ApplyOpID, claim.Requester, claim.BindingID, claim.OldEpochID).Scan(&exact)
	if err != nil || exact != 1 {
		return publicationClaim{}, errors.Join(&IntegrityError{}, err)
	}
	incoming, err := readNativeRepositoryIntentTx(ctx, tx, claim.RepositoryIntentID)
	if err != nil || incoming.hash != claim.RepositoryIntentSHA || incoming.record.PlanID != planID || incoming.record.PointID != claim.PointID || incoming.record.OperationID != claim.ApplyOpID || incoming.record.ConsumerSHA256 != claim.ConsumerSHA || incoming.record.BindingID != claim.BindingID || incoming.record.SourceEpochID != claim.OldEpochID || incoming.epochID != claim.NewEpochID {
		return publicationClaim{}, errors.Join(&IntegrityError{}, err)
	}
	return claim, nil
}

func insertInitialPublicationCursor(ctx context.Context, tx *sql.Tx, cursor datapublish.Cursor) error {
	raw, err := cursor.MarshalJSON()
	if err != nil {
		return ErrInvalid
	}
	id, hash := cursor.Scope().PlanID, publicationStorageSHA256(raw)
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_module_publication_cursors VALUES(?,0,?,?)", id, hash, raw); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_module_publication_cursor_records VALUES(?,0,'',?,?)", id, hash, raw)
	return err
}

func readPublicationCursorTx(ctx context.Context, tx *sql.Tx, plan datapublish.Plan) (datapublish.Cursor, error) {
	claim, err := readPublicationClaimTx(ctx, tx, plan.ID())
	if err != nil {
		return datapublish.Cursor{}, err
	}
	var raw []byte
	var hash string
	var revision uint64
	err = tx.QueryRowContext(ctx, "SELECT revision,record_sha256,canonical FROM data_module_publication_cursors WHERE plan_id=?", plan.ID()).Scan(&revision, &hash, &raw)
	if err != nil {
		return datapublish.Cursor{}, err
	}
	if err = checkPublicationCursorHistoryTx(ctx, tx, plan.ID(), revision, hash, raw); err != nil {
		return datapublish.Cursor{}, &IntegrityError{}
	}
	cursor, err := datapublish.DecodeCursor(raw, plan, hash)
	if err != nil || cursor.Scope().ApplyOpID != claim.ApplyOpID || cursor.Scope().ConsumerClaimSHA256 != claim.ConsumerSHA || cursor.Scope().BindingID != string(claim.BindingID) || cursor.Scope().PointID != claim.PointID || cursor.Scope().OldEpochID != string(claim.OldEpochID) || cursor.Scope().NewEpochID != string(claim.NewEpochID) {
		return datapublish.Cursor{}, &IntegrityError{}
	}
	return cursor, nil
}

func checkPublicationCursorHistoryTx(ctx context.Context, tx *sql.Tx, planID string, revision uint64, hash string, raw []byte) error {
	rows, err := tx.QueryContext(ctx, "SELECT revision,previous_sha256,record_sha256,canonical FROM data_module_publication_cursor_records WHERE plan_id=? ORDER BY revision", planID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var sequence uint64
	var previous string
	for rows.Next() {
		var recordedRevision uint64
		var predecessor, recordedHash string
		var recorded []byte
		if err = rows.Scan(&recordedRevision, &predecessor, &recordedHash, &recorded); err != nil {
			return err
		}
		if recordedRevision != sequence || predecessor != previous || publicationStorageSHA256(recorded) != recordedHash || recordedRevision > revision {
			return &IntegrityError{}
		}
		if recordedRevision == revision && (recordedHash != hash || !bytes.Equal(recorded, raw)) {
			return &IntegrityError{}
		}
		sequence++
		previous = recordedHash
	}
	if rows.Err() != nil || sequence != revision+1 || previous != hash {
		return errors.Join(&IntegrityError{}, rows.Err())
	}
	return nil
}

func appendPublicationCursorTx(ctx context.Context, tx *sql.Tx, plan datapublish.Plan, old, next datapublish.Cursor) error {
	current, err := readPublicationCursorTx(ctx, tx, plan)
	if err != nil {
		return err
	}
	currentRaw, currentErr := current.MarshalJSON()
	oldRaw, oldErr := old.MarshalJSON()
	nextRaw, nextErr := next.MarshalJSON()
	if currentErr != nil || oldErr != nil || nextErr != nil || !bytes.Equal(currentRaw, oldRaw) || bytes.Equal(oldRaw, nextRaw) || old.Scope() != next.Scope() {
		return ErrConflict
	}
	oldHash, nextHash := publicationStorageSHA256(oldRaw), publicationStorageSHA256(nextRaw)
	checked, err := datapublish.DecodeCursor(nextRaw, plan, nextHash)
	if err != nil || checked.Scope() != current.Scope() {
		return ErrInvalid
	}
	var revision uint64
	if err = tx.QueryRowContext(ctx, "SELECT revision FROM data_module_publication_cursors WHERE plan_id=? AND record_sha256=?", plan.ID(), oldHash).Scan(&revision); err != nil {
		return errors.Join(ErrConflict, err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_module_publication_cursor_records VALUES(?,?,?,?,?)", plan.ID(), revision+1, oldHash, nextHash, nextRaw); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE data_module_publication_cursors SET revision=revision+1,record_sha256=?,canonical=? WHERE plan_id=? AND revision=? AND record_sha256=?", nextHash, nextRaw, plan.ID(), revision, oldHash)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func (s *Store) ReadPublicationCursor(ctx context.Context, planID string) (datapublish.Cursor, error) {
	if !digestPattern.MatchString(planID) {
		return datapublish.Cursor{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return datapublish.Cursor{}, err
	}
	defer func() { _ = tx.Rollback() }()
	raw, _, err := readPublicationReviewTx(ctx, tx, planID)
	if err != nil {
		return datapublish.Cursor{}, err
	}
	review, err := datapublish.DecodePrivateReview(raw)
	if err != nil {
		return datapublish.Cursor{}, &IntegrityError{}
	}
	cursor, err := readPublicationCursorTx(ctx, tx, review.Plan())
	if err != nil {
		return datapublish.Cursor{}, err
	}
	return cursor, tx.Commit()
}
