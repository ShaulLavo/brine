package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
)

func (s *Store) CompleteNativeAbandonment(ctx context.Context, abandonment *NativeAbandonment, observed NativeAbandonObservation) (nativecapture.AbandonReceipt, error) {
	if abandonment == nil || abandonment.state != s || !abandonment.active() || !validNativeAbandonObservation(abandonment.facts, observed) {
		return nativecapture.AbandonReceipt{}, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nativecapture.AbandonReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	operation, err := nativeResolutionOperation(ctx, tx, abandonment.operation.ID)
	if err != nil || operation != abandonment.operation || operation.State != ops.Preflight {
		return nativecapture.AbandonReceipt{}, errors.Join(ErrConflict, err)
	}
	facts, err := readNativeAbandonFactsTx(ctx, tx, operation)
	if err != nil || !sameNativeAbandonFacts(abandonment.facts, facts) || !validNativeAbandonObservation(facts, observed) || !abandonment.active() {
		return nativecapture.AbandonReceipt{}, errors.Join(ErrConflict, err)
	}
	wanted, err := appendNativeAbandonStoppedIntent(ctx, tx, operation, facts)
	if err != nil {
		return nativecapture.AbandonReceipt{}, err
	}
	p := facts.point.view
	result, err := tx.ExecContext(ctx, "UPDATE data_fences SET state='released' WHERE id=? AND operation_id=? AND database_id=? AND incarnation_id=? AND state='held'", p.FenceID, p.FenceOwnerOpID, p.DatabaseID, p.IncarnationID)
	if err != nil {
		return nativecapture.AbandonReceipt{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return nativecapture.AbandonReceipt{}, errors.Join(ErrConflict, err)
	}
	stop, err := json.Marshal(observed.Stop)
	if err != nil {
		return nativecapture.AbandonReceipt{}, ErrInvalid
	}
	receipt := nativecapture.AbandonReceipt{Kind: string(ops.NativePrepareAbandon), OperationID: operation.ID, InputSHA256: operation.SecretRef, PointID: p.PointID, PointSHA256: facts.point.hash, PreparationOperationID: p.PreparationOperationID, FenceOwnerOperationID: p.FenceOwnerOpID, FenceID: string(p.FenceID), BindingID: string(p.BindingID), EpochID: string(p.EpochID), SourceSealSHA256: p.SourceSealSHA256, CurrentStopSHA256: nativePointHash(stop), CurrentCodeSHA256: facts.codeSHA, CurrentRoutesSHA256: observed.RoutesSHA256, ReleaseID: facts.release.ID, StoppedDesiredRevision: wanted.Revision, CompletedAt: time.Now().UTC(), Outcome: "abandoned_stopped"}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return nativecapture.AbandonReceipt{}, ErrInvalid
	}
	if err := completeTaskTx(ctx, tx, operation.ID, ops.Succeeded, ops.TaskOutcome{Receipt: raw}); err != nil {
		return nativecapture.AbandonReceipt{}, err
	}
	if !abandonment.active() {
		return nativecapture.AbandonReceipt{}, ErrConflict
	}
	return receipt, tx.Commit()
}

func validNativeAbandonObservation(facts nativeAbandonFacts, observed NativeAbandonObservation) bool {
	p := facts.point.view
	allocation := facts.permit.Allocation
	stop, err := json.Marshal(observed.Stop)
	if err != nil || nativePointHash(stop) != p.StopProofSHA256 {
		return false
	}
	if allocation == nil || !validNativeStop(observed.Stop, p.SourceKind) || observed.SourceSHA256 != p.SourceSealSHA256 || observed.CodeSHA256 != facts.codeSHA || !data.ValidCatalogHash(observed.RoutesSHA256) || observed.CheckpointSHA != p.CheckpointSHA256 || observed.Directory.Device != allocation.DirectoryDevice || observed.Directory.Inode != allocation.DirectoryInode {
		return false
	}
	now := time.Now().UTC()
	return !observed.ObservedAt.IsZero() && observed.ObservedAt.Location() == time.UTC && !observed.ObservedAt.After(now) && now.Sub(observed.ObservedAt) <= time.Minute
}

func appendNativeAbandonStoppedIntent(ctx context.Context, tx *sql.Tx, operation ops.Operation, facts nativeAbandonFacts) (ManagedDesiredState, error) {
	previous := facts.wanted
	if previous.Revision >= 1<<63-1 {
		return ManagedDesiredState{}, ErrConflict
	}
	wanted := ManagedDesiredState{BindingID: facts.point.view.BindingID, EpochID: facts.point.view.EpochID, RuntimeSHA256: facts.point.view.RuntimeSHA256, Revision: previous.Revision + 1, PlanID: facts.release.PlanID, OperationID: operation.ID}
	if !wanted.Valid() {
		return ManagedDesiredState{}, ErrInvalid
	}
	raw, err := json.Marshal(wanted)
	if err != nil {
		return ManagedDesiredState{}, ErrInvalid
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO data_engine_desired_states VALUES(?,?,?,?,?)", wanted.BindingID, wanted.Revision, wanted.PlanID, operation.ID, raw); err != nil {
		return ManagedDesiredState{}, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE data_engine_desired_heads SET revision=? WHERE binding_id=? AND revision=?", wanted.Revision, wanted.BindingID, previous.Revision)
	if err != nil {
		return ManagedDesiredState{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ManagedDesiredState{}, errors.Join(ErrConflict, err)
	}
	return wanted, nil
}
