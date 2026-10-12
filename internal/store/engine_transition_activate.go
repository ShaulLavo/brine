package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

// activateEngineTransitionTx commits a previously admitted target atomically.
// Protected point/consumer and target proof joins belong to its 17/18 caller.
func (s *Store) activateEngineTransitionTx(ctx context.Context, tx *sql.Tx, preparation *EnginePreparation, old data.EngineTransitionCursor, runtime data.EngineRuntime, allocation data.AllocationReceipt) (data.EngineTransitionCursor, error) {
	input, cursor, err := readEngineTransition(ctx, tx, old.ID)
	if err != nil || cursor != old || old.Stage != data.EngineTransitionActivationIntent || !s.transitionGuard(preparation, input) {
		return old, errors.Join(ErrConflict, err)
	}
	if err = checkEngineTransitionFence(ctx, tx, input); err != nil {
		return old, err
	}
	if err = transitionHeadMatches(ctx, tx, input); err != nil {
		return old, err
	}
	common := input.Common()
	reserved, err := readReservedEngine(ctx, tx, common.Scope.BindingID)
	if err != nil || runtime.Validate() != nil || runtime.State != data.EngineInitialized || runtime.BindingID != common.Scope.BindingID || runtime.EpochID != common.NewEpochID ||
		!nativeAllocationMatches(allocation, reserved, common.TargetTree) || allocation.MappingProof.Runtime != runtime.Runtime {
		return old, errors.Join(ErrConflict, err)
	}
	reservation, _, err := readNativeEpoch(ctx, tx, common.Scope.BindingID, common.NewEpochID)
	if err != nil || !reservation.Matches(input) {
		return old, errors.Join(ErrConflict, err)
	}
	module, err := readEngineModule(ctx, tx, common.Scope.DatabaseID, runtime.Module.Program)
	if err != nil || module != runtime.Module {
		return old, errors.Join(ErrConflict, err)
	}
	runtime = runtime.Clone()
	raw, err := json.Marshal(runtime)
	if err != nil || nativeCanonicalHash(raw) != common.TargetRuntimeSHA256 {
		return old, ErrConflict
	}
	version := uint64(1)
	if base, committed := common.Base.Committed(); committed {
		version = base.Version + 1
	}
	head := data.EngineHeadRef{BindingID: runtime.BindingID, EpochID: runtime.EpochID, Version: version, RuntimeSHA256: nativeCanonicalHash(raw)}
	event, err := data.RecordEngineTransitionActivation(input, old, head)
	if err != nil {
		return old, ErrInvalid
	}
	material := nativeEpochMaterial{BindingID: runtime.BindingID, EpochID: runtime.EpochID, RuntimeVersion: version,
		Allocation: cloneNativeAllocation(allocation), Checkpoint: common.TargetCheckpoint, Material: common.TargetMaterial, TransitionID: old.ID}
	if err = insertNativeActivation(ctx, tx, input, old, event, raw, material); err != nil {
		return old, err
	}
	return event.Cursor(), nil
}

func insertNativeActivation(ctx context.Context, tx *sql.Tx, input data.EngineTransition, old data.EngineTransitionCursor, event data.EngineTransitionEvent, runtime []byte, material nativeEpochMaterial) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO data_engine_runtime_commits VALUES(?,?,?)", material.BindingID, material.RuntimeVersion, runtime); err != nil {
		return err
	}
	raw, err := json.Marshal(material)
	if err != nil || len(raw) > 65536 {
		return ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_epoch_materials VALUES(?,?,?,?)", material.BindingID, material.EpochID, material.RuntimeVersion, raw); err != nil {
		return err
	}
	return appendEngineTransitionEvent(ctx, tx, input, old, event)
}

// finishEngineTransitionTx is not public release authority. Its caller resolves
// the actual protected fresh proof before entering this transaction.
func (s *Store) finishEngineTransitionTx(ctx context.Context, tx *sql.Tx, preparation *EnginePreparation, old data.EngineTransitionCursor, proof data.EngineEvidenceRef, abandon bool) (data.EngineTransitionCursor, error) {
	input, cursor, err := readEngineTransition(ctx, tx, old.ID)
	if err != nil || old != cursor || !s.transitionGuard(preparation, input) {
		return old, errors.Join(ErrConflict, err)
	}
	if err = checkEngineTransitionFence(ctx, tx, input); err != nil {
		return old, err
	}
	event, err := nativeFinishEvent(ctx, tx, input, old, proof, abandon)
	if err != nil {
		return old, err
	}
	if err = appendEngineTransitionEvent(ctx, tx, input, old, event); err != nil {
		return old, err
	}
	c := input.Common()
	result, err := tx.ExecContext(ctx, "UPDATE data_fences SET state='released' WHERE id=? AND operation_id=? AND database_id=? AND state='held'", c.FenceID, c.FenceOwnerOperationID, c.Scope.DatabaseID)
	if err != nil {
		return old, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return old, errors.Join(ErrConflict, err)
	}
	return event.Cursor(), nil
}

func nativeFinishEvent(ctx context.Context, tx *sql.Tx, input data.EngineTransition, old data.EngineTransitionCursor, proof data.EngineEvidenceRef, abandon bool) (data.EngineTransitionEvent, error) {
	if abandon {
		if err := transitionHeadMatches(ctx, tx, input); err != nil {
			return data.EngineTransitionEvent{}, err
		}
		return data.RecordEngineTransitionAbandonment(input, old, proof)
	}
	head, _, err := readEngineHead(ctx, tx, input.Common().Scope.BindingID)
	if err != nil || head.EpochID != input.Common().NewEpochID || head.RuntimeSHA256 != input.Common().TargetRuntimeSHA256 {
		return data.EngineTransitionEvent{}, errors.Join(ErrConflict, err)
	}
	return data.RecordEngineTransitionCompletion(input, old, proof)
}
