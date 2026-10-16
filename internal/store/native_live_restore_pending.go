package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/ops"
	"time"
)

func readNativeRestoreTransitionInput(ctx context.Context, q dataQuerier, expected data.EngineTransition, stage liverestore.Stage) (data.EngineTransition, data.EngineTransitionCursor, error) {
	input, cursor, err := readEngineTransition(ctx, q, expected.Common().Scope.ID)
	if errors.Is(err, ErrNotFound) && stage == liverestore.TransitionIntent {
		return expected, data.EngineTransitionCursor{}, nil
	}
	return input, cursor, err
}

func (r *NativeIncomingRepository) checkOperation(ctx context.Context, tx *sql.Tx) error {
	if r.resolveOperationID == "" {
		return checkNativeIncomingRepositoryOperation(ctx, tx, r.consumer)
	}
	original, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", r.consumer.applyOperationID))
	if err != nil || original.Kind != ops.NativeLiveRestore || original.Requester != r.consumer.requester || original.SecretRef != r.consumer.planID {
		return ErrConflict
	}
	return checkNativeRestoreResolutionOperation(ctx, tx, r.resolveOperationID, original)
}

func (s *Store) ResumeNativeRestoreIncomingRepository(ctx context.Context, capability *NativeRestoreTransition) (*NativeIncomingRepository, error) {
	if !capability.active(s) || capability.resolveOperationID == "" {
		return nil, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, _, _, cursor, err := s.readNativeRestoreTransitionTx(ctx, tx, capability)
	if err != nil || cursor.Sequence != 0 {
		return nil, errors.Join(ErrConflict, err)
	}
	incoming, err := readNativeRepositoryIntentByOperation(ctx, tx, capability.attempt.OperationID)
	if err != nil {
		return nil, err
	}
	lease, raw, state, err := readNativeRepositoryCreationLease(ctx, tx, incoming)
	if errors.Is(err, ErrNotFound) {
		id, idErr := data.NewID()
		if idErr != nil {
			return nil, idErr
		}
		lease = nativeRepositoryLeaseRecord{Format: "brine-native-repository-lease-v1", ID: id, OperationID: incoming.record.OperationID, BindingID: incoming.record.BindingID, EpochID: incoming.epochID, IntentID: incoming.creationID, IntentSHA256: incoming.hash, ConsumerSHA256: incoming.record.ConsumerSHA256, CreatedAt: time.Now().UTC()}
		raw, err = json.Marshal(lease)
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO data_native_repository_leases VALUES(?,?,?,?,'held',?)", id, lease.OperationID, lease.BindingID, lease.EpochID, raw)
		state = "held"
	}
	if err != nil || state != "held" && state != "recovery_required" {
		return nil, errors.Join(ErrConflict, err)
	}
	a, spec := capability.attempt, retained.plan.Spec()
	consumer := nativePointConsumer{kind: nativePointLiveRestore, pointID: a.PreparedPointID, planID: a.PlanID, applyOperationID: a.OperationID, requester: spec.Requester.String(), binding: a.BindingID, epoch: a.SourceEpochID, generation: spec.Facts.Generation, generationID: spec.Facts.GenerationID, policySHA256: spec.Facts.PolicySHA256}
	result := &NativeIncomingRepository{store: s, guard: capability.preparation.guard, intent: incoming, consumer: consumer, lease: lease, leaseRaw: raw, resolveOperationID: capability.resolveOperationID}
	if _, err := s.readNativeIncomingRepositoryTx(ctx, tx, result); err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

func (s *Store) PrepareResolvedNativeRestoreTransition(ctx context.Context, capability *NativeRestoreTransition, incoming *NativeIncomingRepository, observed NativeRestoreTransitionObservation) error {
	if !capability.active(s) || capability.resolveOperationID == "" || !incoming.active(s) || incoming.resolveOperationID != capability.resolveOperationID || incoming.intent.record.OperationID != capability.attempt.OperationID {
		return ErrConflict
	}
	initialized, err := s.ReadNativeIncomingInitialized(ctx, incoming)
	if err != nil {
		return err
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	retained, target, input, cursor, err := s.readNativeRestoreTransitionTx(ctx, tx, capability)
	if err != nil || cursor.Sequence != 0 || data.ClassifyEngineTransitionLayout(input, observed.Layout) != data.EngineLayoutPrepared {
		return errors.Join(ErrConflict, err)
	}
	if err = checkNativeRestoreResumeObservation(ctx, capability, retained, target, input, observed); err != nil {
		return err
	}
	intent, err := s.readNativeIncomingRepositoryTx(ctx, tx, incoming)
	if err != nil || intent.epochID != capability.attempt.IncomingEpochID {
		return errors.Join(ErrConflict, err)
	}
	var raw []byte
	var repository string
	if err := tx.QueryRowContext(ctx, "SELECT repository_id,canonical FROM data_native_repository_results WHERE intent_id=?", intent.creationID).Scan(&repository, &raw); err != nil {
		return err
	}
	expected, err := json.Marshal(initialized)
	if err != nil || repository != initialized.RepositoryID || !bytes.Equal(raw, expected) {
		return ErrConflict
	}
	if _, err = s.insertGuardedEngineTransitionTx(ctx, tx, capability.preparation, input, repository); err != nil {
		return err
	}
	return tx.Commit()
}
