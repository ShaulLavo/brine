package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/ops"
)

type publicationClaim struct {
	PlanID              string                `json:"plan_id"`
	PointID             string                `json:"point_id"`
	ApplyOpID           string                `json:"apply_operation_id"`
	Requester           string                `json:"requester"`
	BindingID           data.ReplicaBindingID `json:"binding_id"`
	OldEpochID          data.ReplicaEpochID   `json:"old_epoch_id"`
	NewEpochID          data.ReplicaEpochID   `json:"new_epoch_id"`
	ConsumerSHA         string                `json:"consumer_sha256"`
	RepositoryIntentID  string                `json:"repository_intent_id"`
	RepositoryIntentSHA string                `json:"repository_intent_sha256"`
}

func readNativeModulePublishConsumerTx(ctx context.Context, tx *sql.Tx, pointID, planID, applyOperationID string) (nativePointConsumer, error) {
	raw, index, err := readPublicationReviewTx(ctx, tx, planID)
	if err != nil {
		return nativePointConsumer{}, err
	}
	review, err := datapublish.DecodePrivateReview(raw)
	if err != nil || index.pointID != pointID {
		return nativePointConsumer{}, &IntegrityError{}
	}
	plan := review.Plan()
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", applyOperationID))
	if err != nil || string(op.Kind) != "module_publish_apply" || op.SecretRef != planID || op.App != plan.Facts().App || op.Requester != plan.Requester().String() || op.State != ops.Preflight {
		return nativePointConsumer{}, errors.Join(ErrConflict, err)
	}
	return nativePointConsumer{kind: nativePointModulePublish, pointID: pointID, planID: planID, applyOperationID: op.ID, requester: op.Requester,
		binding: index.binding, epoch: data.ReplicaEpochID(plan.Facts().EpochID), generation: plan.Facts().Generation, generationID: index.generationID, policySHA256: plan.Facts().PolicySHA256}, nil
}

func (s *Store) beginPublicationClaim(ctx context.Context, preparation *EnginePreparation, planID, applyOperationID string, now time.Time) (datapublish.Cursor, error) {
	if s.readOnly || !digestPattern.MatchString(planID) || preparation == nil || preparation.StateRoot() != s.dir {
		return datapublish.Cursor{}, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return datapublish.Cursor{}, err
	}
	defer func() { _ = tx.Rollback() }()
	raw, index, err := readPublicationReviewTx(ctx, tx, planID)
	if err != nil || !preparation.Active(index.binding) {
		return datapublish.Cursor{}, errors.Join(ErrConflict, err)
	}
	review, err := datapublish.DecodePrivateReview(raw)
	if err != nil {
		return datapublish.Cursor{}, &IntegrityError{}
	}
	plan := review.Plan()
	if plan.CheckFreshness(plan.Requester(), plan.Facts(), plan.Source(), now) != nil {
		return datapublish.Cursor{}, ErrConflict
	}
	consumer, err := readNativeModulePublishConsumerTx(ctx, tx, index.pointID, planID, applyOperationID)
	if err != nil {
		return datapublish.Cursor{}, err
	}
	point, err := s.claimNativePreparedPointTx(ctx, tx, preparation.guard, consumer)
	if err != nil || point.hash != index.pointSHA || point.view.RuntimeVersion != index.runtimeVersion || point.view.RuntimeSHA256 != index.runtimeSHA || !publicationPlanMatchesPoint(plan, point.view) {
		return datapublish.Cursor{}, errors.Join(ErrConflict, err)
	}
	existing, err := readPublicationClaimTx(ctx, tx, planID)
	if err == nil {
		if existing.ApplyOpID != applyOperationID || existing.PointID != point.view.PointID || existing.Requester != consumer.requester {
			return datapublish.Cursor{}, ErrConflict
		}
		cursor, err := readPublicationCursorTx(ctx, tx, plan)
		if err != nil {
			return datapublish.Cursor{}, err
		}
		return cursor, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return datapublish.Cursor{}, err
	}
	incoming, err := s.reserveNativeIncomingRepositoryTx(ctx, tx, preparation.guard, consumer)
	if err != nil {
		return datapublish.Cursor{}, err
	}
	claim, cursor, err := newPublicationClaim(plan, consumer, incoming)
	if err != nil {
		return datapublish.Cursor{}, err
	}
	canonical, err := json.Marshal(claim)
	if err != nil {
		return datapublish.Cursor{}, ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_module_publication_claims VALUES(?,?,?,?,?,?,?,?,?,?,?)", claim.PlanID, claim.PointID, claim.ApplyOpID, claim.Requester, claim.BindingID, claim.OldEpochID, claim.NewEpochID, claim.ConsumerSHA, claim.RepositoryIntentID, claim.RepositoryIntentSHA, canonical)
	if err != nil {
		return datapublish.Cursor{}, err
	}
	if err = insertInitialPublicationCursor(ctx, tx, cursor); err != nil || !preparation.Active(index.binding) {
		return datapublish.Cursor{}, errors.Join(ErrConflict, err)
	}
	return cursor, tx.Commit()
}

func newPublicationClaim(plan datapublish.Plan, consumer nativePointConsumer, incoming nativeRepositoryIntent) (publicationClaim, datapublish.Cursor, error) {
	consumerRaw, err := consumer.canonical()
	if err != nil {
		return publicationClaim{}, datapublish.Cursor{}, ErrInvalid
	}
	if incoming.record.PlanID != plan.ID() || incoming.record.PointID != consumer.pointID || incoming.record.OperationID != consumer.applyOperationID || incoming.record.ConsumerSHA256 != publicationStorageSHA256(consumerRaw) || incoming.record.BindingID != consumer.binding || incoming.record.SourceEpochID != consumer.epoch || !data.ValidID(incoming.creationID) || !data.ValidCatalogHash(incoming.hash) || incoming.epochID == consumer.epoch {
		return publicationClaim{}, datapublish.Cursor{}, ErrConflict
	}
	claim := publicationClaim{PlanID: plan.ID(), PointID: consumer.pointID, ApplyOpID: consumer.applyOperationID, Requester: consumer.requester,
		BindingID: consumer.binding, OldEpochID: consumer.epoch, NewEpochID: incoming.epochID, ConsumerSHA: publicationStorageSHA256(consumerRaw), RepositoryIntentID: incoming.creationID, RepositoryIntentSHA: incoming.hash}
	cursor, err := datapublish.NewCursor(plan, claim.ApplyOpID, claim.ConsumerSHA, string(incoming.epochID))
	return claim, cursor, err
}
