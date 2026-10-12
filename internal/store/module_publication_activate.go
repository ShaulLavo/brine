package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
)

// PublicationTransitionObservation is a fresh trusted host read under both
// guards. It is never public approval or an independently decoded proof.
type PublicationTransitionObservation struct {
	Layout               data.EngineLayoutObservation
	UnitSHA256           string
	MaterialSHA256       string
	EngineAbsent         bool
	ApplicationAbsent    bool
	IngressClosed        bool
	PrivateReadersAbsent bool
	ObservedAt           time.Time
}

func (PublicationTransitionObservation) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

func checkPublicationTransitionObservation(target publicationTarget, input data.EngineTransition, observed PublicationTransitionObservation) error {
	if data.ClassifyEngineTransitionLayout(input, observed.Layout) != data.EngineLayoutPromoted || observed.UnitSHA256 != target.runtime.UnitSHA256 || observed.MaterialSHA256 != target.record.MaterialSHA256 || !observed.EngineAbsent || !observed.ApplicationAbsent || !observed.IngressClosed || !observed.PrivateReadersAbsent || observed.ObservedAt.Location() != time.UTC || observed.ObservedAt.Before(target.record.ObservedAt) || observed.ObservedAt.After(time.Now().UTC()) {
		return ErrConflict
	}
	return nil
}

func (s *Store) AdvancePublicationTransition(ctx context.Context, capability *PublicationTransition, next data.EngineTransitionStage, observed data.EngineLayoutObservation) (data.EngineTransitionCursor, error) {
	var zero data.EngineTransitionCursor
	if !capability.active(ctx, s) {
		return zero, ErrConflict
	}
	switch next {
	case data.EngineTransitionPreserveIntent, data.EngineTransitionOriginalPreserved, data.EngineTransitionPromoteIntent, data.EngineTransitionTargetPromoted, data.EngineTransitionActivationIntent:
	default:
		return zero, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	_, _, input, cursor, err := s.readPublicationTransitionTx(ctx, tx, capability)
	if err != nil || checkPublicationTransitionAnchors(ctx, capability, observed) != nil {
		return zero, errors.Join(ErrConflict, err)
	}
	event, err := data.AdvanceEngineTransition(input, cursor, next, observed)
	if err != nil {
		return zero, ErrConflict
	}
	if err = appendEngineTransitionEvent(ctx, tx, input, cursor, event); err != nil {
		return zero, err
	}
	if !capability.active(ctx, s) {
		return zero, ErrConflict
	}
	return event.Cursor(), tx.Commit()
}

func (s *Store) ActivatePublicationTransition(ctx context.Context, capability *PublicationTransition, observed PublicationTransitionObservation) (data.EngineTransitionCursor, error) {
	var zero data.EngineTransitionCursor
	if !capability.active(ctx, s) {
		return zero, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	current, target, input, cursor, err := s.readPublicationTransitionTx(ctx, tx, capability)
	if err != nil || cursor.Stage != data.EngineTransitionActivationIntent || checkPublicationTransitionObservation(target, input, observed) != nil || checkPublicationTransitionAnchors(ctx, capability, observed.Layout) != nil {
		return zero, errors.Join(ErrConflict, err)
	}
	if err = checkPublicationDesiredStoppedTx(ctx, tx, input, target); err != nil {
		return zero, err
	}
	activated, err := s.activateEngineTransitionTx(ctx, tx, capability.preparation.attempt.preparation, cursor, target.runtime, target.record.Allocation)
	if err != nil {
		return zero, err
	}
	if err = recordPublicationStoppedDesiredTx(ctx, tx, current, target); err != nil {
		return zero, err
	}
	if !capability.active(ctx, s) {
		return zero, ErrConflict
	}
	return activated, tx.Commit()
}

func checkPublicationDesiredStoppedTx(ctx context.Context, tx *sql.Tx, input data.EngineTransition, target publicationTarget) error {
	desired, effective, err := readEffectiveManagedDesired(ctx, tx, input.Common().Scope.BindingID, target.runtime.EpochID, target.record.RuntimeSHA256)
	if err != nil || effective && (desired.EngineRunning || desired.FrontendRunning || desired.IngressServing) {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func recordPublicationStoppedDesiredTx(ctx context.Context, tx *sql.Tx, current publicationAttemptState, target publicationTarget) error {
	approval, err := readPublicationReviewApprovalTx(ctx, tx, current.review, current.index)
	if err != nil {
		return err
	}
	previous, err := readManagedDesired(ctx, tx, current.claim.BindingID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	wanted := ManagedDesiredState{BindingID: current.claim.BindingID, EpochID: current.claim.NewEpochID, RuntimeSHA256: target.record.RuntimeSHA256, Revision: previous.Revision + 1, PlanID: approval.TargetDeclarationID, OperationID: current.claim.ApplyOpID}
	if !wanted.Valid() {
		return ErrConflict
	}
	raw, err := json.Marshal(wanted)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_engine_desired_states VALUES(?,?,?,?,?)", wanted.BindingID, wanted.Revision, wanted.PlanID, wanted.OperationID, raw); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_engine_desired_heads VALUES(?,?) ON CONFLICT(binding_id) DO UPDATE SET revision=excluded.revision", wanted.BindingID, wanted.Revision)
	return err
}

func (s *Store) BeginPublicationTransitionCompletion(ctx context.Context, capability *PublicationTransition, observed PublicationTransitionObservation) (data.EngineTransitionCursor, error) {
	var zero data.EngineTransitionCursor
	if !capability.active(ctx, s) {
		return zero, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	_, target, input, cursor, err := s.readPublicationTransitionTx(ctx, tx, capability)
	if err != nil || cursor.Stage != data.EngineTransitionActivated || checkPublicationTransitionObservation(target, input, observed) != nil || checkPublicationTransitionAnchors(ctx, capability, observed.Layout) != nil {
		return zero, errors.Join(ErrConflict, err)
	}
	event, err := data.AdvanceEngineTransition(input, cursor, data.EngineTransitionFinishIntent, observed.Layout)
	if err != nil {
		return zero, err
	}
	if err = appendEngineTransitionEvent(ctx, tx, input, cursor, event); err != nil {
		return zero, err
	}
	if !capability.active(ctx, s) {
		return zero, ErrConflict
	}
	return event.Cursor(), tx.Commit()
}

type publicationCompletionRecord struct {
	Format         string                 `json:"format"`
	Scope          datapublish.ProofScope `json:"scope"`
	TransitionID   string                 `json:"transition_id"`
	InputSHA256    string                 `json:"input_sha256"`
	TargetSHA256   string                 `json:"target_sha256"`
	CursorSHA256   string                 `json:"cursor_sha256"`
	LayoutSHA256   string                 `json:"layout_sha256"`
	UnitSHA256     string                 `json:"unit_sha256"`
	MaterialSHA256 string                 `json:"material_sha256"`
	ObservedAt     time.Time              `json:"observed_at"`
}

func (s *Store) CompletePublicationTransition(ctx context.Context, capability *PublicationTransition, observed PublicationTransitionObservation) (datapublish.Cursor, error) {
	if !capability.active(ctx, s) {
		return datapublish.Cursor{}, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return datapublish.Cursor{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, target, input, cursor, err := s.readPublicationTransitionTx(ctx, tx, capability)
	if err != nil || cursor.Stage != data.EngineTransitionFinishIntent || checkPublicationTransitionObservation(target, input, observed) != nil || checkPublicationTransitionAnchors(ctx, capability, observed.Layout) != nil {
		return current.cursor, errors.Join(ErrConflict, err)
	}
	if err = checkPublicationDesiredStoppedTx(ctx, tx, input, target); err != nil {
		return current.cursor, err
	}
	publicationRaw, _ := current.cursor.MarshalJSON()
	layoutRaw, _ := json.Marshal(observed.Layout)
	record := publicationCompletionRecord{Format: "brine-publication-completion-v1", Scope: current.cursor.Scope(), TransitionID: input.Common().Scope.ID, InputSHA256: input.SHA256(), TargetSHA256: target.hash, CursorSHA256: publicationStorageSHA256(publicationRaw), LayoutSHA256: publicationStorageSHA256(layoutRaw), UnitSHA256: observed.UnitSHA256, MaterialSHA256: observed.MaterialSHA256, ObservedAt: observed.ObservedAt}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 65536 {
		return current.cursor, ErrInvalid
	}
	if err = insertPublicationProofTx(ctx, tx, current.review.Plan().ID(), "completion", "transition_completion", raw); err != nil {
		return current.cursor, err
	}
	hash := publicationStorageSHA256(raw)
	common := input.Common()
	proof := data.EngineEvidenceRef{Kind: data.EngineCompletionEvidence, ID: common.Scope.ID, SHA256: hash, DatabaseID: common.Scope.DatabaseID, BindingID: common.Scope.BindingID, EpochID: common.NewEpochID}
	if _, err = s.finishEngineTransitionTx(ctx, tx, capability.preparation.attempt.preparation, cursor, proof, false); err != nil {
		return current.cursor, err
	}
	next, err := current.cursor.Observe(publicationTargetObservation(current.cursor, datapublish.FinalTransition, hash))
	if err != nil {
		return current.cursor, err
	}
	if err = appendPublicationCursorTx(ctx, tx, current.review.Plan(), current.cursor, next); err != nil {
		return current.cursor, err
	}
	if !capability.active(ctx, s) {
		return current.cursor, ErrConflict
	}
	return next, tx.Commit()
}
