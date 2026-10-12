package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
)

func checkPublicationStageDisposedTx(ctx context.Context, tx *sql.Tx, attempt *PublicationAttempt, cursor datapublish.Cursor, step datapublish.Step, process PublicationReviewObservation) ([]byte, error) {
	var nonce string
	if err := tx.QueryRowContext(ctx, "SELECT nonce FROM data_module_publication_effects WHERE plan_id=? AND operation_id=? AND step=?", attempt.planID, attempt.operationID, step).Scan(&nonce); err != nil {
		return nil, err
	}
	effect, err := readPublicationEffectTx(ctx, tx, nonce)
	if err != nil || effect.Scope != cursor.Scope() || effect.Step != step || attempt.restored.unchanged(ctx) != nil {
		return nil, errors.Join(ErrConflict, err)
	}
	actual, err := os.Lstat(effect.RootPath)
	identity, identityErr := data.InspectRoot(effect.RootPath)
	if err != nil || identityErr != nil || !os.SameFile(actual, attempt.restored.identity) || effect.RootIdentity != (data.EngineDirectoryIdentity{Device: identity.Device, Inode: identity.Inode}) {
		return nil, errors.Join(ErrConflict, err, identityErr)
	}
	runtime, _ := json.Marshal(effect.Runtime)
	expected, _ := json.Marshal(process.Runtime)
	if !bytes.Equal(runtime, expected) || !process.Disposed || process.StartedAt.Before(effect.RecordedAt) {
		return nil, ErrConflict
	}
	events, err := readPublicationEffectEventsTx(ctx, tx, effect)
	if err != nil || len(events) < 2 || events[0].Stage != "started" || events[0].PID != process.HelperPID || events[0].StartTicks != process.HelperStartTicks || events[0].Cgroup != process.HelperCgroup || events[0].RecordedAt.Before(process.StartedAt) {
		return nil, errors.Join(ErrConflict, err)
	}
	for _, event := range events {
		if event.Stage == "disposed" && !event.RecordedAt.After(process.FinishedAt) {
			return json.Marshal(struct {
				Effect PublicationEffect        `json:"effect"`
				Events []PublicationEffectEvent `json:"events"`
			}{effect, events})
		}
	}
	return nil, ErrConflict
}

func (s *Store) RecordPublicationDisposed(ctx context.Context, attempt *PublicationAttempt) (datapublish.Cursor, error) {
	if !attempt.active(s) || attempt.restored.unchanged(ctx) != nil {
		return datapublish.Cursor{}, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return datapublish.Cursor{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.readPublicationAttemptTx(ctx, tx, attempt)
	if err != nil || current.cursor.State() != datapublish.IntentRecorded || current.cursor.IntendedStep() != datapublish.DisposeStage {
		return datapublish.Cursor{}, errors.Join(ErrConflict, err)
	}
	verified, verifiedSHA, err := readPublicationVerificationTx(ctx, tx, attempt, current)
	if err != nil {
		return current.cursor, err
	}
	published, publicationSHA, err := readPublicationPublishedTx(ctx, tx, current.cursor)
	if err != nil || publicationSHA != verified.PublicationSHA256 {
		return current.cursor, errors.Join(&IntegrityError{}, err)
	}
	publication, err := checkPublicationStageDisposedTx(ctx, tx, attempt, current.cursor, datapublish.PublishStage, published.Process)
	if err != nil {
		return current.cursor, err
	}
	preflight, err := readPublicationPreflightDisposalTx(ctx, tx, attempt, current)
	if err != nil {
		return current.cursor, err
	}
	raw, err := json.Marshal(struct {
		Scope              datapublish.ProofScope `json:"scope"`
		VerificationSHA256 string                 `json:"verification_sha256"`
		Publication        json.RawMessage        `json:"publication"`
		Preflight          json.RawMessage        `json:"preflight"`
		ReaderNonce        string                 `json:"reader_nonce"`
		ReaderViewSHA256   string                 `json:"reader_view_sha256"`
		ReaderImageID      string                 `json:"reader_image_id"`
		ReaderDisposedAt   time.Time              `json:"reader_disposed_at"`
	}{current.cursor.Scope(), verifiedSHA, publication, preflight, verified.ReaderNonce, verified.ReaderViewSHA256, verified.ReaderImageID, verified.ReaderDisposedAt})
	if err != nil {
		return current.cursor, ErrInvalid
	}
	next, err := current.cursor.Observe(publicationTargetObservation(current.cursor, datapublish.DisposeStage, publicationStorageSHA256(raw)))
	if err != nil {
		return current.cursor, ErrConflict
	}
	if err = insertPublicationProofTx(ctx, tx, attempt.planID, "disposal", "staging_disposal", raw); err != nil {
		return current.cursor, err
	}
	if err = appendPublicationCursorTx(ctx, tx, current.review.Plan(), current.cursor, next); err != nil || attempt.restored.unchanged(ctx) != nil {
		return current.cursor, errors.Join(ErrConflict, err)
	}
	return next, tx.Commit()
}

func readPublicationPreflightDisposalTx(ctx context.Context, tx *sql.Tx, attempt *PublicationAttempt, current publicationAttemptState) ([]byte, error) {
	if current.review.Plan().Source().Kind() == datapublish.CreateSource {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM data_module_publication_effects WHERE plan_id=? AND step=?", attempt.planID, datapublish.CheckPreflight).Scan(&count); err != nil || count != 0 {
			return nil, errors.Join(ErrConflict, err)
		}
		return []byte("null"), nil
	}
	raw, _, err := readPublicationProofTx(ctx, tx, attempt.planID, "preflight", "preflight")
	var process PublicationReviewObservation
	if err != nil || json.Unmarshal(raw, &process) != nil {
		return nil, errors.Join(&IntegrityError{}, err)
	}
	canonical, _ := json.Marshal(process)
	if !bytes.Equal(raw, canonical) || !bytes.Equal(process.Preflight, current.review.NativePreflightResponse()) {
		return nil, &IntegrityError{}
	}
	return checkPublicationStageDisposedTx(ctx, tx, attempt, current.cursor, datapublish.CheckPreflight, process)
}

func readPublicationVerificationTx(ctx context.Context, tx *sql.Tx, attempt *PublicationAttempt, current publicationAttemptState) (publicationVerificationRecord, string, error) {
	var record publicationVerificationRecord
	raw, hash, err := readPublicationProofTx(ctx, tx, attempt.planID, "verification", "target_verification")
	if err != nil || json.Unmarshal(raw, &record) != nil {
		return record, "", errors.Join(&IntegrityError{}, err)
	}
	canonical, _ := json.Marshal(record)
	if !bytes.Equal(raw, canonical) || record.Format != "brine-publication-target-verification-v1" || record.Scope != current.cursor.Scope() || record.VerifiedAt.Location() != time.UTC || record.VerifiedAt.Before(record.ReaderDisposedAt) || record.VerifiedAt.After(time.Now().UTC()) {
		return record, "", &IntegrityError{}
	}
	checkpoint, err := nativecapture.DecodeCheckpoint(record.Checkpoint, nativecapture.DefaultLimits())
	evidence, evidenceErr := nativeevidence.Decode(record.Evidence, nativeevidence.MaximumLimits())
	published, publicationSHA, publicationErr := readPublicationPublishedTx(ctx, tx, current.cursor)
	observed := PublicationVerificationObservation{Checkpoint: checkpoint, Evidence: evidence, ReaderNonce: record.ReaderNonce, ReaderViewSHA256: record.ReaderViewSHA256, ReaderImageID: record.ReaderImageID, ReaderDisposedAt: record.ReaderDisposedAt}
	if err != nil || evidenceErr != nil || publicationErr != nil || publicationSHA != record.PublicationSHA256 || checkPublicationVerificationObservation(ctx, attempt, current, published, observed) != nil {
		return record, "", &IntegrityError{}
	}
	approval, err := readPublicationReviewApprovalTx(ctx, tx, current.review, current.index)
	approvalRaw, _ := json.Marshal(approval)
	if err != nil || publicationStorageSHA256(approvalRaw) != record.ApprovalSHA256 {
		return record, "", &IntegrityError{}
	}
	bundle, err := readNativePreparedBundleTx(ctx, tx, current.point.view.PointID)
	if err != nil {
		return record, "", err
	}
	dataProof, requirements, err := derivePublicationRequirementProofs(current.review.Plan(), bundle.sourceEvidence, evidence, record.ApprovalSHA256)
	expected, _ := json.Marshal(requirements)
	actual, _ := json.Marshal(record.Requirements)
	if err != nil || !bytes.Equal(dataProof, record.DataProof) || !bytes.Equal(expected, actual) {
		return record, "", &IntegrityError{}
	}
	return record, hash, nil
}
