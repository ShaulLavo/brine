package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/datapublish"
)

func (s *Store) RecordPublicationPreflight(ctx context.Context, attempt *PublicationAttempt, observed PublicationReviewObservation) (datapublish.Cursor, error) {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return datapublish.Cursor{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.readPublicationAttemptTx(ctx, tx, attempt)
	if err != nil || current.cursor.State() != datapublish.IntentRecorded || current.cursor.IntendedStep() != datapublish.CheckPreflight {
		return datapublish.Cursor{}, errors.Join(ErrConflict, err)
	}
	approval, err := readPublicationReviewApprovalTx(ctx, tx, current.review, current.index)
	if err != nil {
		return current.cursor, err
	}
	proofSHA := current.review.Plan().Preflight().ResponseSHA256
	if current.point.view.SourceKind == "initialized" {
		if !observedPublicationPreflightMatches(observed, approval.RestoreVerifiedAt) || !bytes.Equal(observed.Preflight, current.review.NativePreflightResponse()) || observed.Publisher != current.point.view.NativePublisher {
			return current.cursor, ErrConflict
		}
	} else {
		if checkObservedInitialReviewOwner(ctx, tx, current.point, observed) != nil || len(observed.Preflight) != 0 {
			return current.cursor, ErrConflict
		}
		proofSHA = current.point.view.SourceWitnessSHA256
	}
	permit, err := readEnginePermit(ctx, tx, current.point.view.BindingID)
	actual, _ := json.Marshal(permit.Runtime)
	expected, _ := json.Marshal(observed.Runtime)
	if err != nil || !bytes.Equal(actual, expected) {
		return current.cursor, errors.Join(ErrConflict, err)
	}
	raw, err := json.Marshal(observed)
	if err != nil || len(raw) > 4<<20 {
		return current.cursor, ErrInvalid
	}
	next, err := current.cursor.Observe(publicationOriginalObservation(current.cursor, current.point.view, datapublish.CheckPreflight, proofSHA))
	if err != nil {
		return current.cursor, ErrConflict
	}
	if err = insertPublicationProofTx(ctx, tx, current.review.Plan().ID(), "preflight", publicationPreflightProof, raw); err != nil {
		return current.cursor, err
	}
	if err = appendPublicationCursorTx(ctx, tx, current.review.Plan(), current.cursor, next); err != nil {
		return current.cursor, err
	}
	return next, tx.Commit()
}
