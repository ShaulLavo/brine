package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/policy"
)

func (s *Store) BeginNativeLiveRestore(ctx context.Context, stopped *NativeStoppedPreparation, planID, operationID string, pol policy.Policy) (liverestore.Event, error) {
	if s.readOnly || !stopped.active(s) || !pol.AllowsLiveRestore() {
		return liverestore.Event{}, ErrConflict
	}
	if _, err := s.ReadNativeStoppedPermit(ctx, stopped); err != nil {
		return liverestore.Event{}, err
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return liverestore.Event{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, c, err := readNativeLiveRestoreAdmissionTx(ctx, tx, stopped, planID, operationID, pol)
	if err != nil {
		return liverestore.Event{}, err
	}
	point, err := s.claimNativePreparedPointTx(ctx, tx, stopped.preparation.preparation.guard, c)
	if err != nil {
		return liverestore.Event{}, err
	}
	old, err := readNativeLiveRestoreAttempt(ctx, tx, operationID)
	if err == nil {
		if old.plan.ID() != p.ID() || old.claim.Attempt().PreparedPointID != point.view.PointID {
			return liverestore.Event{}, ErrConflict
		}
		return old.current, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return liverestore.Event{}, err
	}
	incoming, err := s.reserveNativeIncomingRepositoryTx(ctx, tx, stopped.preparation.preparation.guard, c)
	if err != nil {
		return liverestore.Event{}, err
	}
	raw, err := c.canonical()
	if err != nil {
		return liverestore.Event{}, ErrInvalid
	}
	transitionID, err := data.NewID()
	if err != nil {
		return liverestore.Event{}, err
	}
	a := liverestore.Attempt{PlanID: p.ID(), OperationID: operationID, TransitionID: transitionID, ConsumerSHA256: nativePointHash(raw), PreparedPointID: point.view.PointID, SelectedPointID: p.Spec().Selected.PointID, FenceID: point.view.FenceID, FenceOwnerOpID: point.view.FenceOwnerOpID, BindingID: point.view.BindingID, SourceEpochID: point.view.EpochID, IncomingEpochID: incoming.epochID}
	event, err := liverestore.BeginAttempt(p, a)
	if err != nil {
		return liverestore.Event{}, ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_live_restore_claims VALUES(?,?,?,?,?,?,?)", operationID, p.ID(), a.PreparedPointID, a.BindingID, a.IncomingEpochID, a.TransitionID, event.Bytes())
	if err != nil {
		return liverestore.Event{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_live_restore_events VALUES(?,?,?,?)", operationID, event.Sequence(), event.Stage(), event.Bytes())
	if err != nil || !stopped.active(s) {
		return liverestore.Event{}, errors.Join(ErrConflict, err)
	}
	return event, tx.Commit()
}

func readNativeLiveRestoreAdmissionTx(ctx context.Context, tx *sql.Tx, stopped *NativeStoppedPreparation, planID, operationID string, pol policy.Policy) (liverestore.Plan, nativePointConsumer, error) {
	p, err := readNativeLiveRestorePlanTx(ctx, tx, planID)
	if err != nil {
		return p, nativePointConsumer{}, err
	}
	spec := p.Spec()
	if stopped.PointID() != spec.Safety.PointID || stopped.OperationID() != spec.Safety.FenceOwnerOpID || !pol.AllowsLiveRestore() || strings.TrimPrefix(pol.Hash(), "sha256:") != spec.Facts.PolicySHA256 {
		return p, nativePointConsumer{}, ErrConflict
	}
	point, err := readNativePreparedPointTx(ctx, tx, spec.Safety.PointID)
	if err != nil || point.view.SourceKind != "initialized" || checkNativePointCurrent(ctx, tx, point.view) != nil || point.view.StopProofSHA256 != stopped.stopSHA256 {
		return p, nativePointConsumer{}, errors.Join(ErrConflict, err)
	}
	facts, safety, err := readNativeLiveRestoreCurrentTx(ctx, tx, point)
	if err != nil {
		return p, nativePointConsumer{}, err
	}
	bundle, err := readNativeVerifiedPointBundleTx(ctx, tx, spec.Selected.PointID)
	if err != nil {
		return p, nativePointConsumer{}, err
	}
	selected := nativeLiveRestoreSelected(bundle)
	if selected.NativeDatabase != point.view.NativeDatabase || selected.NativePublisher != point.view.NativePublisher {
		return p, nativePointConsumer{}, ErrConflict
	}
	recovery, err := readNativeLiveRestoreRecoveryTx(ctx, tx, facts.App, spec.Recovery.ReleaseID, selected)
	if err != nil || recovery != spec.Recovery || p.CheckFreshness(spec.Requester, facts, safety, selected, time.Now().UTC()) != nil {
		return p, nativePointConsumer{}, errors.Join(ErrConflict, err)
	}
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", operationID))
	if err != nil || op.Kind != ops.NativeLiveRestore || op.State != ops.Preflight || op.SecretRef != planID || op.Requester != spec.Requester.String() || op.App != facts.App {
		return p, nativePointConsumer{}, errors.Join(ErrConflict, err)
	}
	c := nativePointConsumer{kind: nativePointLiveRestore, pointID: point.view.PointID, planID: planID, applyOperationID: operationID, requester: op.Requester, binding: facts.Head.BindingID, epoch: facts.Head.EpochID, generation: facts.Generation, generationID: facts.GenerationID, policySHA256: facts.PolicySHA256}
	return p, c, nil
}

func (s *Store) RecordNativeLiveRestoreDownloadIntent(ctx context.Context, stopped *NativeStoppedPreparation, operationID string, pol policy.Policy) (*NativeLiveRestoreExecution, error) {
	if s.readOnly || !stopped.active(s) {
		return nil, ErrConflict
	}
	if _, err := s.ReadNativeStoppedPermit(ctx, stopped); err != nil {
		return nil, err
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, operationID)
	if err != nil {
		return nil, err
	}
	if _, _, err = readNativeLiveRestoreAdmissionTx(ctx, tx, stopped, retained.plan.ID(), operationID, pol); err != nil {
		return nil, err
	}
	next, err := liverestore.RecordIntent(retained.plan, retained.current, liverestore.RestoreIntent)
	if err != nil {
		return nil, ErrConflict
	}
	if err = s.appendNativeLiveRestoreEventTx(ctx, tx, stopped.preparation.preparation, retained.current, next); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.ReadNativeLiveRestoreExecution(ctx, stopped, operationID)
}
