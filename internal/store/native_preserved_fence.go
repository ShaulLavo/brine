package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/ops"
)

func (s *Store) holdPreservedUnpublishedPreparationFence(ctx context.Context, binding data.ReplicaBindingID, operation string) (data.QuiescenceFence, error) {
	reserved, err := s.ReadReservedEngine(ctx, binding)
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	guard, err := s.guardEngineDatabase(ctx, reserved.Database.DatabaseID)
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	defer func() { _ = guard.Release() }()
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	defer func() { _ = tx.Rollback() }()
	op, err := nativeResolutionOperation(ctx, tx, operation)
	if err != nil || op.Kind != ops.NativePrepare || op.State != ops.Preflight {
		return data.QuiescenceFence{}, errors.Join(ErrConflict, err)
	}
	input, err := readNativeTaskInput(ctx, tx, op)
	if err != nil || input.BindingID != binding {
		return data.QuiescenceFence{}, errors.Join(ErrConflict, err)
	}
	if err = refusePendingNativePreparation(ctx, tx, binding, operation); err != nil {
		return data.QuiescenceFence{}, err
	}
	permit, err := readEnginePermit(ctx, tx, binding)
	if err != nil || permit.Runtime.State != data.EngineUnpublished || permit.Allocation == nil {
		return data.QuiescenceFence{}, errors.Join(ErrConflict, err)
	}
	authority, err := readUnpublishedPreparationAuthority(ctx, tx, binding, permit.Reserved.Database, permit.Runtime)
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	if err = checkPreservedUnpublishedConsumers(ctx, tx, permit, authority, op); err != nil {
		return data.QuiescenceFence{}, err
	}
	fence, err := s.holdDataFenceTx(ctx, tx, permit.Reserved.Database.DatabaseID, operation)
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	return fence, tx.Commit()
}

func checkPreservedUnpublishedConsumers(ctx context.Context, tx *sql.Tx, permit EnginePermit, authority unpublishedPreparationAuthority, op ops.Operation) error {
	var released int
	err := tx.QueryRowContext(ctx, "SELECT count(*) FROM data_fences WHERE id=? AND operation_id=? AND database_id=? AND incarnation_id=? AND state='released'", authority.fence, authority.operation, permit.Reserved.Database.DatabaseID, permit.Reserved.Database.IncarnationID).Scan(&released)
	if err != nil || released != 1 {
		return errors.Join(ErrConflict, err)
	}
	rows, err := tx.QueryContext(ctx, "SELECT point_id,kind,plan_id,apply_operation_id FROM data_native_point_consumers WHERE binding_id=? AND epoch_id=?", permit.Runtime.BindingID, permit.Runtime.EpochID)
	if err != nil {
		return err
	}
	type consumer struct{ point, kind, plan, operation string }
	consumers := make([]consumer, 0)
	for rows.Next() {
		var current consumer
		if err = rows.Scan(&current.point, &current.kind, &current.plan, &current.operation); err != nil || len(consumers) >= 128 {
			_ = rows.Close()
			return errors.Join(ErrConflict, err)
		}
		consumers = append(consumers, current)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if len(consumers) == 0 {
		return ErrConflict
	}
	desired, err := readManagedDesired(ctx, tx, permit.Runtime.BindingID)
	if err != nil {
		return err
	}
	desiredJoined := false
	for _, consumer := range consumers {
		if consumer.kind != string(nativePointModulePublish) {
			return ErrConflict
		}
		if _, err = readNativePointConsumer(ctx, tx, consumer.point); err != nil {
			return err
		}
		current, err := readPreservedPublicationForPreparation(ctx, tx, consumer.plan, consumer.point, consumer.operation, permit, op)
		if err != nil {
			return err
		}
		if err = checkPreservedPublicationCompletion(ctx, tx, current); err != nil {
			return err
		}
		approval, err := readPublicationReviewApprovalTx(ctx, tx, current.review, current.index)
		if err != nil {
			return err
		}
		desiredJoined = desiredJoined || desired.OperationID == current.claim.ApplyOpID && desired.PlanID == approval.TargetDeclarationID
	}
	if !desiredJoined {
		return ErrConflict
	}
	return nil
}

func readPreservedPublicationForPreparation(ctx context.Context, tx *sql.Tx, planID, pointID, originalID string, permit EnginePermit, op ops.Operation) (publicationAttemptState, error) {
	var current publicationAttemptState
	raw, index, err := readPublicationReviewTx(ctx, tx, planID)
	if err != nil {
		return current, err
	}
	current.review, err = datapublish.DecodePrivateReview(raw)
	current.index = index
	if err != nil {
		return current, &IntegrityError{}
	}
	current.claim, err = readPublicationClaimTx(ctx, tx, planID)
	if err != nil || current.claim.PointID != pointID || current.claim.ApplyOpID != originalID || current.claim.BindingID != permit.Runtime.BindingID || current.claim.OldEpochID != permit.Runtime.EpochID {
		return current, errors.Join(ErrConflict, err)
	}
	current.point, err = readNativePreparedPointTx(ctx, tx, pointID)
	if err != nil || current.point.hash != index.pointSHA || !publicationPlanMatchesPoint(current.review.Plan(), current.point.view) {
		return current, errors.Join(&IntegrityError{}, err)
	}
	current.cursor, err = readPublicationCursorTx(ctx, tx, current.review.Plan())
	if err != nil || current.cursor.State() != datapublish.SourcePreserved || current.review.Plan().Facts().App != op.App || current.review.Plan().Requester().String() != op.Requester {
		return current, errors.Join(ErrConflict, err)
	}
	head, runtime, err := readEngineHead(ctx, tx, permit.Runtime.BindingID)
	actual, _ := json.Marshal(runtime)
	expected, _ := json.Marshal(permit.Runtime)
	if err != nil || head.RuntimeSHA256 != index.runtimeSHA || head.Version != index.runtimeVersion || head.EpochID != current.claim.OldEpochID || !bytes.Equal(actual, expected) {
		return current, errors.Join(ErrConflict, err)
	}
	desired, err := readManagedDesired(ctx, tx, permit.Runtime.BindingID)
	if err != nil || !desired.Current(permit.Runtime.BindingID, permit.Runtime.EpochID, head.RuntimeSHA256) || desired.EngineRunning || desired.FrontendRunning || desired.IngressServing {
		return current, errors.Join(ErrConflict, err)
	}
	var released int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_fences WHERE id=? AND operation_id=? AND database_id=? AND incarnation_id=? AND state='released'", current.point.view.FenceID, current.point.view.FenceOwnerOpID, current.point.view.DatabaseID, current.point.view.IncarnationID).Scan(&released)
	if err != nil || released != 1 {
		return current, errors.Join(ErrConflict, err)
	}
	return current, nil
}

func checkPreservedPublicationCompletion(ctx context.Context, tx *sql.Tx, current publicationAttemptState) error {
	raw, hash, err := readPublicationProofTx(ctx, tx, current.claim.PlanID, "source_preservation", "source_preservation")
	var record publicationSourcePreservationRecord
	if err != nil || json.Unmarshal(raw, &record) != nil || current.cursor.SourcePreservationProofSHA256() != hash {
		return errors.Join(&IntegrityError{}, err)
	}
	original, err := nativeResolutionOperation(ctx, tx, current.claim.ApplyOpID)
	if err != nil {
		return errors.Join(ErrConflict, err)
	}
	var originalOutcome []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", original.ID).Scan(&originalOutcome); err != nil {
		return err
	}
	if _, err = ops.DecodeTaskOutcome(original, originalOutcome); err != nil || publicationStorageSHA256(originalOutcome) != record.OriginalOutcomeSHA256 {
		return errors.Join(&IntegrityError{}, err)
	}
	resolver, err := nativeResolutionOperation(ctx, tx, record.ResolverID)
	if err != nil || !preservedPublicationOperationsMatch(current.cursor.Scope(), current.review.Plan().Facts().App, current.claim.Requester, original, resolver, record) {
		return errors.Join(ErrConflict, err)
	}
	receipt, err := publicationPreservedReceiptTx(ctx, tx, current, resolver.ID)
	if err != nil || receipt.CompletionSHA256 != hash {
		return errors.Join(&IntegrityError{}, err)
	}
	expected, _ := json.Marshal(receipt)
	var outcomeRaw []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", resolver.ID).Scan(&outcomeRaw); err != nil {
		return err
	}
	outcome, err := ops.DecodeTaskOutcome(resolver, outcomeRaw)
	if err != nil || !bytes.Equal(outcome.Receipt, expected) {
		return errors.Join(&IntegrityError{}, err)
	}
	return nil
}

func preservedPublicationOperationsMatch(scope datapublish.ProofScope, app, requester string, original, resolver ops.Operation, record publicationSourcePreservationRecord) bool {
	return record.Scope == scope && record.ResolverID == resolver.ID && original.ID == scope.ApplyOpID &&
		original.Kind == ops.ModulePublishApply && original.State == ops.RecoveryRequired && original.SecretRef == scope.PlanID && original.App == app && original.Requester == requester &&
		resolver.Kind == ops.ModulePublishResolve && resolver.State == ops.Succeeded && resolver.RecoveryOf == original.ID && resolver.SecretRef == original.SecretRef && resolver.App == original.App && resolver.Requester == original.Requester
}
