package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/ops"
)

// NativeLiveRestoreExecution ties a retained restore intent to its original
// stopped preparation. Decoded plans and journal events cannot construct it.
type NativeLiveRestoreExecution struct {
	state            *Store
	stopped          *NativeStoppedPreparation
	event            liverestore.Event
	binding          data.SpacetimeDBBinding
	checkpointSHA256 string
	sourceSealSHA256 string
}

func (NativeLiveRestoreExecution) String() string               { return "[private live restore execution]" }
func (NativeLiveRestoreExecution) GoString() string             { return "[private live restore execution]" }
func (NativeLiveRestoreExecution) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

func (p *NativeLiveRestoreExecution) Attempt() liverestore.Attempt {
	if p == nil {
		return liverestore.Attempt{}
	}
	return p.event.Attempt()
}

func (p *NativeLiveRestoreExecution) Binding() data.SpacetimeDBBinding {
	if p == nil {
		return data.SpacetimeDBBinding{}
	}
	return p.binding
}

func (p *NativeLiveRestoreExecution) IntentSHA256() string {
	if p == nil {
		return ""
	}
	return p.event.SHA256()
}

func (p *NativeLiveRestoreExecution) CheckpointSHA256() string {
	if p == nil {
		return ""
	}
	return p.checkpointSHA256
}

func (p *NativeLiveRestoreExecution) SourceSealSHA256() string {
	if p == nil {
		return ""
	}
	return p.sourceSealSHA256
}

func (s *Store) ReadNativeLiveRestoreExecution(ctx context.Context, stopped *NativeStoppedPreparation, operationID string) (*NativeLiveRestoreExecution, error) {
	if s.readOnly || !stopped.active(s) {
		return nil, ErrConflict
	}
	if _, err := s.ReadNativeStoppedPermit(ctx, stopped); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, operationID)
	if err != nil {
		return nil, err
	}
	if err = checkNativeLiveRestoreExecutionTx(ctx, tx, stopped, retained); err != nil {
		return nil, err
	}
	reserved, err := readReservedEngine(ctx, tx, retained.claim.Attempt().BindingID)
	if err != nil || !stopped.active(s) {
		return nil, errors.Join(ErrConflict, err)
	}
	capability := &NativeLiveRestoreExecution{state: s, stopped: stopped, event: retained.current, binding: reserved.Database, checkpointSHA256: retained.plan.Spec().Safety.CheckpointSHA256, sourceSealSHA256: retained.plan.Spec().Safety.SourceSealSHA256}
	return capability, tx.Commit()
}

func checkNativeLiveRestoreExecutionTx(ctx context.Context, tx *sql.Tx, stopped *NativeStoppedPreparation, retained nativeLiveRestoreAttempt) error {
	if retained.current.Stage() != liverestore.RestoreIntent {
		return ErrConflict
	}
	return checkNativeLiveRestoreSourceTx(ctx, tx, stopped, retained)
}

func checkNativeLiveRestoreSourceTx(ctx context.Context, tx *sql.Tx, stopped *NativeStoppedPreparation, retained nativeLiveRestoreAttempt) error {
	a, spec := retained.claim.Attempt(), retained.plan.Spec()
	if stopped.PointID() != a.PreparedPointID || stopped.OperationID() != a.FenceOwnerOpID {
		return ErrConflict
	}
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", a.OperationID))
	if err != nil || op.Kind != ops.NativeLiveRestore || op.SecretRef != a.PlanID || op.Requester != spec.Requester.String() || op.App != spec.Facts.App || op.State != ops.Preflight {
		return errors.Join(ErrConflict, err)
	}
	point, err := readNativePreparedPointTx(ctx, tx, a.PreparedPointID)
	if err != nil || point.hash != spec.Safety.PointSHA256 || point.view.BindingID != a.BindingID || point.view.EpochID != a.SourceEpochID || point.view.FenceID != a.FenceID || point.view.FenceOwnerOpID != a.FenceOwnerOpID || point.view.StopProofSHA256 != stopped.stopSHA256 || point.view.SourceSealSHA256 != spec.Safety.SourceSealSHA256 || point.view.SourceWitnessSHA256 != spec.Safety.SourceWitnessSHA256 || point.view.CheckpointSHA256 != spec.Safety.CheckpointSHA256 || point.view.StopProofSHA256 != spec.Safety.StopProofSHA256 {
		return errors.Join(ErrConflict, err)
	}
	if err = checkNativePointCurrent(ctx, tx, point.view); err != nil {
		return err
	}
	facts, safety, err := readNativeLiveRestoreCurrentTx(ctx, tx, point)
	if err != nil || facts != spec.Facts || safety != spec.Safety {
		return errors.Join(ErrConflict, err)
	}
	bundle, err := readNativeVerifiedPointBundleTx(ctx, tx, a.SelectedPointID)
	if err != nil || nativeLiveRestoreSelected(bundle) != spec.Selected {
		return errors.Join(ErrConflict, err)
	}
	recovery, err := readNativeLiveRestoreRecoveryTx(ctx, tx, facts.App, spec.Recovery.ReleaseID, spec.Selected)
	if err != nil || recovery != spec.Recovery {
		return errors.Join(ErrConflict, err)
	}
	c := nativePointConsumer{kind: nativePointLiveRestore, pointID: a.PreparedPointID, planID: a.PlanID, applyOperationID: a.OperationID, requester: spec.Requester.String(), binding: a.BindingID, epoch: a.SourceEpochID, generation: spec.Facts.Generation, generationID: spec.Facts.GenerationID, policySHA256: spec.Facts.PolicySHA256}
	wanted, err := c.canonical()
	if err != nil {
		return ErrInvalid
	}
	actual, err := readNativePointConsumer(ctx, tx, a.PreparedPointID)
	if err != nil || !bytes.Equal(wanted, actual) || nativePointHash(actual) != a.ConsumerSHA256 {
		return errors.Join(ErrConflict, err)
	}
	incoming, err := readNativeRepositoryIntentByOperation(ctx, tx, a.OperationID)
	if err != nil || incoming.record.ConsumerSHA256 != a.ConsumerSHA256 || incoming.record.PlanID != a.PlanID || incoming.epochID != a.IncomingEpochID {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func (s *Store) CheckNativeLiveRestoreExecution(ctx context.Context, capability *NativeLiveRestoreExecution) error {
	if capability == nil || capability.state != s {
		return ErrConflict
	}
	current, err := s.ReadNativeLiveRestoreExecution(ctx, capability.stopped, capability.event.Attempt().OperationID)
	if err != nil || current.event.SHA256() != capability.event.SHA256() || current.binding != capability.binding {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
