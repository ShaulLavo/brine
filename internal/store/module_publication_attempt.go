package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
)

// PublicationAttempt keeps the actual held preparation attached to one retained
// point consumer. Its constructor rereads protected state; a decoded cursor or
// caller-supplied owner, root or proof cannot manufacture this capability.
type PublicationAttempt struct {
	store       *Store
	preparation *EnginePreparation
	planID      string
	operationID string
	binding     data.ReplicaBindingID
	restored    *publicationReviewRestore
}

type publicationAttemptState struct {
	review datapublish.PrivateReview
	index  publicationReviewBinding
	claim  publicationClaim
	point  nativePreparedPoint
	cursor datapublish.Cursor
}

func (a *PublicationAttempt) PlanID() string {
	if a == nil {
		return ""
	}
	return a.planID
}

func (a *PublicationAttempt) OperationID() string {
	if a == nil {
		return ""
	}
	return a.operationID
}

func (s *Store) ReadPublicationAttemptReview(ctx context.Context, attempt *PublicationAttempt) (datapublish.PrivateReview, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return datapublish.PrivateReview{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.readPublicationAttemptTx(ctx, tx, attempt)
	if err != nil {
		return datapublish.PrivateReview{}, err
	}
	return current.review, tx.Commit()
}

func (s *Store) RecordPublicationIntent(ctx context.Context, attempt *PublicationAttempt, step datapublish.Step) (datapublish.Cursor, error) {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return datapublish.Cursor{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.readPublicationAttemptTx(ctx, tx, attempt)
	if err != nil {
		return datapublish.Cursor{}, err
	}
	if step == datapublish.FinalTransition || checkNativePointCurrent(ctx, tx, current.point.view) != nil || checkPublicationPolicy(ctx, tx, current.review.Plan().Facts()) != nil {
		return current.cursor, ErrConflict
	}
	next, err := current.cursor.Intend(step)
	if err != nil {
		return current.cursor, ErrConflict
	}
	if err = appendPublicationCursorTx(ctx, tx, current.review.Plan(), current.cursor, next); err != nil {
		return current.cursor, err
	}
	return next, tx.Commit()
}

func (s *Store) BeginPublicationAttempt(ctx context.Context, preparation *EnginePreparation, planID, operationID string, now time.Time) (*PublicationAttempt, error) {
	cursor, err := s.beginPublicationClaim(ctx, preparation, planID, operationID, now)
	if err != nil {
		return nil, err
	}
	attempt := &PublicationAttempt{store: s, preparation: preparation, planID: planID, operationID: operationID, binding: data.ReplicaBindingID(cursor.Scope().BindingID)}
	if _, err = s.ReadPublicationAttemptCursor(ctx, attempt); err != nil {
		return nil, err
	}
	return attempt, nil
}

func (a *PublicationAttempt) active(s *Store) bool {
	return a != nil && a.store == s && !s.readOnly && a.preparation.Active(a.binding) && a.preparation.StateRoot() == s.dir
}

func (s *Store) readPublicationAttemptTx(ctx context.Context, tx *sql.Tx, attempt *PublicationAttempt) (publicationAttemptState, error) {
	var current publicationAttemptState
	if !attempt.active(s) {
		return current, ErrConflict
	}
	raw, index, err := readPublicationReviewTx(ctx, tx, attempt.planID)
	if err != nil || index.binding != attempt.binding {
		return current, errors.Join(ErrConflict, err)
	}
	current.review, err = datapublish.DecodePrivateReview(raw)
	if err != nil {
		return current, &IntegrityError{}
	}
	current.index = index
	current.claim, err = readPublicationClaimTx(ctx, tx, attempt.planID)
	if err != nil || current.claim.ApplyOpID != attempt.operationID || current.claim.BindingID != attempt.binding || current.claim.PointID != index.pointID {
		return current, errors.Join(ErrConflict, err)
	}
	current.point, err = readNativePreparedPointTx(ctx, tx, index.pointID)
	if err != nil || current.point.hash != index.pointSHA || !publicationPlanMatchesPoint(current.review.Plan(), current.point.view) {
		return current, errors.Join(&IntegrityError{}, err)
	}
	current.cursor, err = readPublicationCursorTx(ctx, tx, current.review.Plan())
	if err != nil || !attempt.active(s) {
		return current, errors.Join(ErrConflict, err)
	}
	return current, nil
}

func (s *Store) ReadPublicationAttemptCursor(ctx context.Context, attempt *PublicationAttempt) (datapublish.Cursor, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return datapublish.Cursor{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.readPublicationAttemptTx(ctx, tx, attempt)
	if err != nil {
		return datapublish.Cursor{}, err
	}
	return current.cursor, tx.Commit()
}

// RecordPublicationUnknown retains the same attempt for inspection. It never
// returns the cursor to Ready or permits the host to issue the step again.
func (s *Store) RecordPublicationUnknown(ctx context.Context, attempt *PublicationAttempt) (datapublish.Cursor, error) {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return datapublish.Cursor{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.readPublicationAttemptTx(ctx, tx, attempt)
	if err != nil {
		return datapublish.Cursor{}, err
	}
	next, err := current.cursor.MarkUnknown()
	if err != nil {
		return current.cursor, ErrConflict
	}
	if err = appendPublicationCursorTx(ctx, tx, current.review.Plan(), current.cursor, next); err != nil {
		return current.cursor, err
	}
	return next, tx.Commit()
}
