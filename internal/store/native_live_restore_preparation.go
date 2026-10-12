package store

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/liverestore"
)

func (s *Store) RecordNativeLiveRestoreTransitionIntent(ctx context.Context, capability *NativeLiveRestoreExecution) (liverestore.Event, error) {
	if s.readOnly || capability == nil || capability.state != s || !capability.stopped.active(s) {
		return liverestore.Event{}, ErrConflict
	}
	if _, err := s.ReadNativeStoppedPermit(ctx, capability.stopped); err != nil {
		return liverestore.Event{}, err
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return liverestore.Event{}, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, capability.Attempt().OperationID)
	if err != nil || retained.claim.Attempt() != capability.Attempt() || retained.current.Stage() != liverestore.Verified {
		return liverestore.Event{}, errors.Join(ErrConflict, err)
	}
	if err = checkNativeLiveRestoreSourceTx(ctx, tx, capability.stopped, retained); err != nil {
		return liverestore.Event{}, err
	}
	if _, _, err = readNativeLiveRestoreVerifiedTx(ctx, tx, retained); err != nil {
		return liverestore.Event{}, err
	}
	next, err := liverestore.RecordIntent(retained.plan, retained.current, liverestore.TransitionIntent)
	if err != nil {
		return liverestore.Event{}, ErrConflict
	}
	if err = s.appendNativeLiveRestoreEventTx(ctx, tx, capability.stopped.preparation.preparation, retained.current, next); err != nil {
		return liverestore.Event{}, err
	}
	return next, tx.Commit()
}

func (s *Store) ClaimNativeLiveRestoreIncomingRepository(ctx context.Context, capability *NativeLiveRestoreExecution) (*NativeIncomingRepository, error) {
	if s.readOnly || capability == nil || capability.state != s || !capability.stopped.active(s) {
		return nil, ErrConflict
	}
	if _, err := s.ReadNativeStoppedPermit(ctx, capability.stopped); err != nil {
		return nil, err
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, capability.Attempt().OperationID)
	if err != nil || retained.claim.Attempt() != capability.Attempt() || retained.current.Stage() != liverestore.TransitionIntent {
		return nil, errors.Join(ErrConflict, err)
	}
	if err = checkNativeLiveRestoreSourceTx(ctx, tx, capability.stopped, retained); err != nil {
		return nil, err
	}
	if _, _, err = readNativeLiveRestoreVerifiedTx(ctx, tx, retained); err != nil {
		return nil, err
	}
	incoming, err := readNativeRepositoryIntentByOperation(ctx, tx, capability.Attempt().OperationID)
	if err != nil {
		return nil, err
	}
	spec, a := retained.plan.Spec(), retained.claim.Attempt()
	consumer := nativePointConsumer{kind: nativePointLiveRestore, pointID: a.PreparedPointID, planID: a.PlanID, applyOperationID: a.OperationID, requester: spec.Requester.String(), binding: a.BindingID, epoch: a.SourceEpochID, generation: spec.Facts.Generation, generationID: spec.Facts.GenerationID, policySHA256: spec.Facts.PolicySHA256}
	created, err := s.claimNativeRepositoryCreationTx(ctx, tx, capability.stopped.preparation.preparation.guard, consumer, incoming)
	if err != nil || !capability.stopped.active(s) {
		return nil, errors.Join(ErrConflict, err)
	}
	return created, tx.Commit()
}
