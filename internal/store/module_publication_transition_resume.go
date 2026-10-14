package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/ops"
)

type PublicationRecoveryFacts struct {
	Point         NativePreparedPointView
	Binding       data.SpacetimeDBBinding
	SourcePermit  EnginePermit
	TargetRuntime data.EngineRuntime
	Input         data.EngineTransition
	Cursor        data.EngineTransitionCursor
	DeclarationID string
}

func (PublicationRecoveryFacts) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

// PublicationTransitionRecoveryFacts reads only the original retained claim.
// The returned expectation requires fresh locks and host observation to resume.
func (s *Store) PublicationTransitionRecoveryFacts(ctx context.Context, planID, originalID, resolverID string) (PublicationRecoveryFacts, error) {
	zero := PublicationRecoveryFacts{}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := readPublicationRecoveryStateTx(ctx, tx, planID, originalID, resolverID)
	if err != nil {
		return zero, err
	}
	target, err := readPublicationTargetTx(ctx, tx, current)
	if err != nil {
		return zero, err
	}
	input, cursor, err := readEngineTransition(ctx, tx, current.claim.RepositoryIntentID)
	expected, inputErr := publicationTransitionInput(ctx, tx, current, target)
	if err != nil || inputErr != nil || input.SHA256() != expected.SHA256() {
		return zero, errors.Join(&IntegrityError{}, err, inputErr)
	}
	permit, err := readPublicationSourcePermitTx(ctx, tx, current)
	if err != nil {
		return zero, err
	}
	approval, err := readPublicationReviewApprovalTx(ctx, tx, current.review, current.index)
	if err != nil {
		return zero, err
	}
	point := current.point.view
	point.PointSHA256 = current.point.hash
	facts := PublicationRecoveryFacts{Point: point, Binding: permit.Reserved.Database, SourcePermit: permit, TargetRuntime: target.runtime.Clone(), Input: input, Cursor: cursor, DeclarationID: approval.TargetDeclarationID}
	return facts, tx.Commit()
}

func readPublicationRecoveryStateTx(ctx context.Context, tx *sql.Tx, planID, originalID, resolverID string) (publicationAttemptState, error) {
	var current publicationAttemptState
	var err error
	current.claim, err = readPublicationClaimTx(ctx, tx, planID)
	if err != nil || current.claim.ApplyOpID != originalID {
		return current, errors.Join(ErrConflict, err)
	}
	original, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", originalID))
	if err != nil || original.Kind != ops.ModulePublishApply || original.State != ops.RecoveryRequired || original.SecretRef != planID || original.Requester != current.claim.Requester {
		return current, errors.Join(ErrConflict, err)
	}
	resolver, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", resolverID))
	if err != nil || resolver.Kind != ops.ModulePublishResolve || resolver.State != ops.Preflight || resolver.RecoveryOf != originalID || resolver.SecretRef != planID || resolver.App != original.App || resolver.Requester != original.Requester {
		return current, errors.Join(ErrConflict, err)
	}
	var outcome []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", originalID).Scan(&outcome); err != nil {
		return current, err
	}
	if _, err = ops.DecodeTaskOutcome(original, outcome); err != nil {
		return current, &IntegrityError{}
	}
	raw, index, err := readPublicationReviewTx(ctx, tx, planID)
	if err != nil {
		return current, err
	}
	current.review, err = datapublish.DecodePrivateReview(raw)
	if err != nil {
		return current, &IntegrityError{}
	}
	current.index = index
	current.point, err = readNativePreparedPointTx(ctx, tx, index.pointID)
	if err != nil || current.point.hash != index.pointSHA || !publicationPlanMatchesPoint(current.review.Plan(), current.point.view) {
		return current, errors.Join(&IntegrityError{}, err)
	}
	current.cursor, err = readPublicationCursorTx(ctx, tx, current.review.Plan())
	if err != nil || checkPublicationTransitionReviewTx(ctx, tx, current) != nil {
		return current, errors.Join(ErrConflict, err)
	}
	return current, nil
}

// ResumePublicationTransition admits only the already recorded 16 transition,
// with the original outcome, protected point and actual fresh host observations.
// It does not reopen expiry-based preparation, publication or repository init.
func (s *Store) ResumePublicationTransition(ctx context.Context, preparation *EnginePreparation, lifetime *data.EngineLock, planID, originalID, resolverID string, observed PublicationTransitionObservation) (*PublicationTransition, error) {
	if preparation == nil || !preparation.Active(preparation.binding) || !lifetime.HoldsLifetime(s.dir, preparation.binding) {
		return nil, ErrConflict
	}
	attempt, err := s.ResumePublicationAttempt(ctx, preparation, planID, originalID, resolverID)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.readPublicationAttemptTx(ctx, tx, attempt)
	if err != nil || current.cursor.IntendedStep() != datapublish.FinalTransition {
		return nil, errors.Join(ErrConflict, err)
	}
	intent, hash, err := readPublicationTargetIntentTx(ctx, tx, planID)
	if err != nil || intent.TransitionID != current.claim.RepositoryIntentID || intent.Scope != current.cursor.Scope() {
		return nil, errors.Join(ErrConflict, err)
	}
	input, _, err := readEngineTransition(ctx, tx, intent.TransitionID)
	if err != nil {
		return nil, err
	}
	capability := &PublicationTransition{preparation: &PublicationTargetPreparation{state: s, attempt: attempt, lifetime: lifetime, intent: intent, intentSHA: hash}, inputSHA256: input.SHA256(), retainedResolver: true}
	_, target, input, cursor, err := s.readPublicationTransitionTx(ctx, tx, capability)
	if err != nil || checkPublicationTransitionAnchors(ctx, capability, observed.Layout) != nil {
		return nil, errors.Join(ErrConflict, err)
	}
	layout := data.ClassifyEngineTransitionLayout(input, observed.Layout)
	if layout == data.EngineLayoutConflict || !observed.EngineAbsent || !observed.ApplicationAbsent || !observed.IngressClosed || !observed.PrivateReadersAbsent || observed.UnitAbsent != target.record.UnitAbsent || observed.UnitSHA256 != publicationTargetUnitSHA256(target) || observed.MaterialSHA256 != target.record.MaterialSHA256 || observed.ObservedAt.Location() != time.UTC || observed.ObservedAt.Before(target.record.ObservedAt) || observed.ObservedAt.After(time.Now().UTC()) {
		return nil, ErrConflict
	}
	if (cursor.Stage == data.EngineTransitionActivated || cursor.Stage == data.EngineTransitionFinishIntent) && layout != data.EngineLayoutPromoted {
		return nil, ErrConflict
	}
	return capability, tx.Commit()
}
