package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/ops"
)

func (s *Store) PublicationDeclarationID(ctx context.Context, planID, requester string) (string, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	raw, index, err := readPublicationReviewTx(ctx, tx, planID)
	if err != nil || index.requester != requester {
		return "", errors.Join(ErrConflict, err)
	}
	review, err := datapublish.DecodePrivateReview(raw)
	if err != nil {
		return "", &IntegrityError{}
	}
	approval, err := readPublicationReviewApprovalTx(ctx, tx, review, index)
	if err != nil {
		return "", err
	}
	return approval.TargetDeclarationID, tx.Commit()
}

func (s *Store) PublicationInspection(ctx context.Context, planID, requester string) (datapublish.Inspection, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return datapublish.Inspection{}, err
	}
	defer func() { _ = tx.Rollback() }()
	raw, index, err := readPublicationReviewTx(ctx, tx, planID)
	if err != nil || index.requester != requester {
		return datapublish.Inspection{}, errors.Join(ErrConflict, err)
	}
	review, err := datapublish.DecodePrivateReview(raw)
	if err != nil {
		return datapublish.Inspection{}, &IntegrityError{}
	}
	if _, err = readPublicationReviewApprovalTx(ctx, tx, review, index); err != nil {
		return datapublish.Inspection{}, err
	}
	view := datapublish.Inspection{PlanID: planID, App: review.Plan().Facts().App, PointID: index.pointID, State: "reviewed"}
	claim, err := readPublicationClaimTx(ctx, tx, planID)
	if errors.Is(err, sql.ErrNoRows) {
		return view, tx.Commit()
	}
	if err != nil {
		return datapublish.Inspection{}, err
	}
	cursor, err := readPublicationCursorTx(ctx, tx, review.Plan())
	if err != nil {
		return datapublish.Inspection{}, err
	}
	view.OperationID, view.State, view.NeedsInspection = claim.ApplyOpID, cursor.State(), cursor.State() == datapublish.IntentRecorded || cursor.State() == datapublish.InspectRequired
	view.Step = cursor.IntendedStep()
	if step, ready := cursor.NextStep(); ready {
		view.Step = step
	}
	_, transition, err := readEngineTransition(ctx, tx, claim.RepositoryIntentID)
	if err == nil {
		view.TransitionState = string(transition.Stage)
	} else if !errors.Is(err, ErrNotFound) {
		return datapublish.Inspection{}, err
	}
	if !view.Valid() {
		return datapublish.Inspection{}, &IntegrityError{}
	}
	return view, tx.Commit()
}

func (s *Store) PublicationCompletionReceipt(ctx context.Context, capability *PublicationTransition) (datapublish.TaskReceipt, error) {
	if !capability.active(s) {
		return datapublish.TaskReceipt{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return datapublish.TaskReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	a := capability.preparation.attempt
	current, err := s.readPublicationAttemptTx(ctx, tx, a)
	if err != nil || current.cursor.State() != datapublish.Finished {
		return datapublish.TaskReceipt{}, errors.Join(ErrConflict, err)
	}
	input, cursor, err := readEngineTransition(ctx, tx, current.claim.RepositoryIntentID)
	if err != nil || input.SHA256() != capability.inputSHA256 || cursor.Stage != data.EngineTransitionCompleted {
		return datapublish.TaskReceipt{}, errors.Join(ErrConflict, err)
	}
	raw, hash, err := readPublicationProofTx(ctx, tx, a.planID, "completion", "transition_completion")
	var completed publicationCompletionRecord
	if err != nil || json.Unmarshal(raw, &completed) != nil || completed.Scope != current.cursor.Scope() || completed.InputSHA256 != input.SHA256() || completed.TransitionID != current.claim.RepositoryIntentID {
		return datapublish.TaskReceipt{}, errors.Join(&IntegrityError{}, err)
	}
	candidate := current.review.Plan().Candidate()
	scope := current.cursor.Scope()
	operation, kind := a.operationID, ops.ModulePublishApply
	if a.resolverID != "" {
		operation, kind = a.resolverID, ops.ModulePublishResolve
	}
	receipt := datapublish.TaskReceipt{Format: "brine-module-publication-task-v1", Kind: string(kind), OperationID: operation, ApplyOperationID: a.operationID, PlanID: a.planID, PointID: scope.PointID, BindingID: scope.BindingID, OldEpochID: scope.OldEpochID, NewEpochID: scope.NewEpochID, TransitionID: input.Common().Scope.ID, TransitionSHA256: input.SHA256(), TransitionState: string(cursor.Stage), CompletionSHA256: hash, Outcome: "published", NativeDatabase: current.cursor.NativeDatabase(), NativePublisher: current.cursor.NativePublisher(), ProgramWireHash: candidate.ProgramWireHash(), WasmSHA256: candidate.WasmSHA256(), SchemaSHA256: candidate.Schema().SHA256()}
	encoded, _ := json.Marshal(receipt)
	if _, err = datapublish.DecodeTaskReceipt(encoded); err != nil {
		return datapublish.TaskReceipt{}, &IntegrityError{}
	}
	return receipt, tx.Commit()
}
