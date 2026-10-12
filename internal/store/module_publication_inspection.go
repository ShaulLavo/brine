package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/strictjson"
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
	if cursor.State() == datapublish.SourcePreserved {
		view.TransitionState = "not_admitted"
	}
	if !view.Valid() {
		return datapublish.Inspection{}, &IntegrityError{}
	}
	return view, tx.Commit()
}

func (s *Store) PublicationCompletionReceipt(ctx context.Context, capability *PublicationTransition) (datapublish.TaskReceipt, error) {
	if !capability.active(ctx, s) {
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
	receipt, err := publicationCompletionReceiptTx(ctx, tx, current, a.resolverID, capability.inputSHA256)
	if err != nil {
		return datapublish.TaskReceipt{}, err
	}
	return receipt, tx.Commit()
}

// PublicationTerminalResolution reads the actual completed transition through
// the resolver/original outcome join. No held-fence preparation is reopened and
// no host effect or fence release is repeated after a lost completion response.
func (s *Store) PublicationTerminalResolution(ctx context.Context, planID, originalID, resolverID string) (datapublish.TaskReceipt, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return datapublish.TaskReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := readPublicationRecoveryStateTx(ctx, tx, planID, originalID, resolverID)
	if err != nil {
		return datapublish.TaskReceipt{}, false, err
	}
	if current.cursor.State() == datapublish.SourcePreserved {
		receipt, err := publicationPreservedReceiptTx(ctx, tx, current, resolverID)
		if err != nil {
			return datapublish.TaskReceipt{}, false, err
		}
		return receipt, true, tx.Commit()
	}
	if current.cursor.State() != datapublish.Finished {
		return datapublish.TaskReceipt{}, false, tx.Commit()
	}
	input, _, err := readEngineTransition(ctx, tx, current.claim.RepositoryIntentID)
	if err != nil {
		return datapublish.TaskReceipt{}, false, err
	}
	target, err := readPublicationTargetTx(ctx, tx, current)
	if err != nil {
		return datapublish.TaskReceipt{}, false, err
	}
	expected, err := publicationTransitionInput(ctx, tx, current, target)
	if err != nil || expected.SHA256() != input.SHA256() {
		return datapublish.TaskReceipt{}, false, errors.Join(&IntegrityError{}, err)
	}
	receipt, err := publicationCompletionReceiptTx(ctx, tx, current, resolverID, input.SHA256())
	if err != nil {
		return datapublish.TaskReceipt{}, false, err
	}
	return receipt, true, tx.Commit()
}

func publicationCompletionReceiptTx(ctx context.Context, tx *sql.Tx, current publicationAttemptState, resolverID, inputSHA256 string) (datapublish.TaskReceipt, error) {
	if current.cursor.State() != datapublish.Finished {
		return datapublish.TaskReceipt{}, ErrConflict
	}
	input, cursor, err := readEngineTransition(ctx, tx, current.claim.RepositoryIntentID)
	if err != nil || input.SHA256() != inputSHA256 || cursor.Stage != data.EngineTransitionCompleted {
		return datapublish.TaskReceipt{}, errors.Join(ErrConflict, err)
	}
	raw, hash, err := readPublicationProofTx(ctx, tx, current.claim.PlanID, "completion", "transition_completion")
	var completed publicationCompletionRecord
	if err != nil || json.Unmarshal(raw, &completed) != nil || completed.Scope != current.cursor.Scope() || completed.InputSHA256 != input.SHA256() || completed.TransitionID != current.claim.RepositoryIntentID {
		return datapublish.TaskReceipt{}, errors.Join(&IntegrityError{}, err)
	}
	canonical, _ := json.Marshal(completed)
	if !bytes.Equal(canonical, raw) || completed.Format != "brine-publication-completion-v1" || completed.ObservedAt.Location() != time.UTC || completed.ObservedAt.Before(current.review.Plan().ExpiresAt().Add(-datapublish.MaxReviewLifetime)) || completed.ObservedAt.After(time.Now().UTC()) {
		return datapublish.TaskReceipt{}, &IntegrityError{}
	}
	if err = checkPublicationCompletionJoinTx(ctx, tx, current, input, cursor, completed, hash); err != nil {
		return datapublish.TaskReceipt{}, err
	}
	candidate := current.review.Plan().Candidate()
	scope := current.cursor.Scope()
	operation, kind := current.claim.ApplyOpID, ops.ModulePublishApply
	if resolverID != "" {
		operation, kind = resolverID, ops.ModulePublishResolve
	}
	receipt := datapublish.TaskReceipt{Format: "brine-module-publication-task-v1", Kind: string(kind), OperationID: operation, ApplyOperationID: current.claim.ApplyOpID, PlanID: current.claim.PlanID, PointID: scope.PointID, BindingID: scope.BindingID, OldEpochID: scope.OldEpochID, NewEpochID: scope.NewEpochID, TransitionID: input.Common().Scope.ID, TransitionSHA256: input.SHA256(), TransitionState: string(cursor.Stage), CompletionSHA256: hash, Outcome: "published", NativeDatabase: current.cursor.NativeDatabase(), NativePublisher: current.cursor.NativePublisher(), ProgramWireHash: candidate.ProgramWireHash(), WasmSHA256: candidate.WasmSHA256(), SchemaSHA256: candidate.Schema().SHA256()}
	encoded, _ := json.Marshal(receipt)
	if _, err = datapublish.DecodeTaskReceipt(encoded); err != nil {
		return datapublish.TaskReceipt{}, &IntegrityError{}
	}
	return receipt, nil
}

func checkPublicationCompletionJoinTx(ctx context.Context, tx *sql.Tx, current publicationAttemptState, input data.EngineTransition, cursor data.EngineTransitionCursor, completed publicationCompletionRecord, hash string) error {
	target, err := readPublicationTargetTx(ctx, tx, current)
	if err != nil || completed.TargetSHA256 != target.hash || completed.UnitSHA256 != target.runtime.UnitSHA256 || completed.MaterialSHA256 != target.record.MaterialSHA256 || completed.ObservedAt.Before(target.record.ObservedAt) {
		return errors.Join(&IntegrityError{}, err)
	}
	var previousRaw []byte
	var previousHash string
	err = tx.QueryRowContext(ctx, "SELECT r.record_sha256,r.canonical FROM data_module_publication_cursors h JOIN data_module_publication_cursor_records r ON r.plan_id=h.plan_id AND r.revision=h.revision-1 WHERE h.plan_id=?", current.claim.PlanID).Scan(&previousHash, &previousRaw)
	previous, decodeErr := datapublish.DecodeCursor(previousRaw, current.review.Plan(), previousHash)
	if err != nil || decodeErr != nil || completed.CursorSHA256 != previousHash || previous.IntendedStep() != datapublish.FinalTransition || previous.Scope() != current.cursor.Scope() {
		return errors.Join(&IntegrityError{}, err, decodeErr)
	}
	var terminalRaw, finishRaw []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_engine_transition_events WHERE transition_id=? AND sequence=?", cursor.ID, cursor.Sequence).Scan(&terminalRaw); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_engine_transition_events WHERE transition_id=? AND sequence=?", cursor.ID, cursor.Sequence-1).Scan(&finishRaw); err != nil {
		return err
	}
	terminal, err := strictjson.Object(terminalRaw, "format", "id", "sequence", "stage", "input_sha256", "previous_sha256", "evidence_kind", "evidence")
	finish, finishErr := strictjson.Object(finishRaw, "format", "id", "sequence", "stage", "input_sha256", "previous_sha256", "evidence_kind", "evidence")
	var proof data.EngineEvidenceRef
	common := input.Common()
	expected := data.EngineEvidenceRef{Kind: data.EngineCompletionEvidence, ID: common.Scope.ID, SHA256: hash, DatabaseID: common.Scope.DatabaseID, BindingID: common.Scope.BindingID, EpochID: common.NewEpochID}
	if err != nil || finishErr != nil || json.Unmarshal(terminal["evidence"], &proof) != nil || proof != expected || publicationStorageSHA256(finish["evidence"]) != completed.LayoutSHA256 {
		return errors.Join(&IntegrityError{}, err, finishErr)
	}
	var released int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_fences WHERE id=? AND operation_id=? AND database_id=? AND incarnation_id=? AND state='released'", common.FenceID, common.FenceOwnerOperationID, common.Scope.DatabaseID, common.Scope.IncarnationID).Scan(&released)
	if err != nil || released != 1 {
		return errors.Join(&IntegrityError{}, err)
	}
	return nil
}
