package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type nativeRestoreTarget struct {
	record     nativeLiveRestoreTargetRecord
	hash       string
	runtime    data.EngineRuntime
	allocation data.AllocationReceipt
	checkpoint nativecapture.Checkpoint
}

func readNativeRestoreTargetTx(ctx context.Context, tx *sql.Tx, retained nativeLiveRestoreAttempt) (nativeRestoreTarget, error) {
	var target nativeRestoreTarget
	var id, operation string
	var raw, runtimeRaw, allocationRaw, checkpointRaw []byte
	err := tx.QueryRowContext(ctx, "SELECT id,operation_id,runtime,allocation,checkpoint,canonical FROM data_native_live_restore_targets WHERE operation_id=?", retained.claim.Attempt().OperationID).Scan(&id, &operation, &runtimeRaw, &allocationRaw, &checkpointRaw, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return target, ErrNotFound
	}
	if err != nil {
		return target, err
	}
	if err = decodeNativeRestoreTargetRecord(raw, &target.record); err != nil {
		return target, err
	}
	r := target.record
	if r.ID != id || operation != r.Attempt.OperationID || r.Attempt != retained.claim.Attempt() || r.RuntimeSHA256 != nativePointHash(runtimeRaw) || r.AllocationSHA256 != nativePointHash(allocationRaw) || r.CheckpointSHA256 != nativePointHash(checkpointRaw) {
		return target, &IntegrityError{}
	}
	target.runtime, err = decodeNativeRestoreRuntime(runtimeRaw)
	if err != nil || len(allocationRaw) == 0 || len(allocationRaw) > 16384 || json.Unmarshal(allocationRaw, &target.allocation) != nil {
		return target, &IntegrityError{}
	}
	canonical, _ := json.Marshal(target.allocation)
	if !bytes.Equal(canonical, allocationRaw) {
		return target, &IntegrityError{}
	}
	target.checkpoint, err = nativecapture.DecodeCheckpoint(checkpointRaw, nativecapture.DefaultLimits())
	if err != nil || !bytes.Equal(target.checkpoint.Bytes(), checkpointRaw) {
		return target, &IntegrityError{}
	}
	verified, verifiedHash, err := readNativeLiveRestoreVerifiedTx(ctx, tx, retained)
	if err != nil || verifiedHash != r.VerifiedSHA256 {
		return target, errors.Join(&IntegrityError{}, err)
	}
	facts, err := readNativeRestoreTargetFactsTx(ctx, tx, retained)
	wanted, _ := json.Marshal(facts.Runtime)
	if err != nil || !bytes.Equal(runtimeRaw, wanted) || facts.Checkpoint.SHA256() != r.CheckpointSHA256 {
		return target, errors.Join(&IntegrityError{}, err)
	}
	permit, err := readNativeRestoreSourcePermitTx(ctx, tx, retained)
	if err != nil {
		return target, err
	}
	observed := NativeRestoreTargetObservation{OldTree: r.OldTree, TargetTree: r.TargetTree, Allocation: target.allocation, UnitSHA256: target.runtime.UnitSHA256, MaterialSHA256: r.MaterialSHA256, ObservedAt: r.ObservedAt}
	if checkNativeRestoreTargetObservation(permit, facts, verified, observed) != nil {
		return target, &IntegrityError{}
	}
	previous, err := readNativeRestoreVerifiedEventTx(ctx, tx, retained)
	if err != nil {
		return target, err
	}
	intent, err := readNativeLiveRestorePhaseTx(ctx, tx, retained, liverestore.TransitionIntent, &previous)
	if err != nil || intent.SHA256() != r.IntentSHA256 {
		return target, errors.Join(&IntegrityError{}, err)
	}
	target.hash = nativePointHash(raw)
	target.allocation = cloneNativeAllocation(target.allocation)
	return target, nil
}

func readNativeRestoreVerifiedEventTx(ctx context.Context, tx *sql.Tx, retained nativeLiveRestoreAttempt) (liverestore.Event, error) {
	initial := retained.claim
	intent, err := readNativeLiveRestorePhaseTx(ctx, tx, retained, liverestore.RestoreIntent, &initial)
	if err != nil {
		return liverestore.Event{}, err
	}
	return readNativeLiveRestorePhaseTx(ctx, tx, retained, liverestore.Verified, &intent)
}

func decodeNativeRestoreTargetRecord(raw []byte, record *nativeLiveRestoreTargetRecord) error {
	if len(raw) == 0 || len(raw) > 65536 {
		return &IntegrityError{}
	}
	fields := []string{"format", "id", "attempt", "verified_sha256", "intent_sha256", "runtime_sha256", "allocation_sha256", "checkpoint_sha256", "material_sha256", "old_tree", "target_tree", "observed_at"}
	if _, err := strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, record) != nil {
		return &IntegrityError{}
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(raw, canonical) || record.Format != "brine-native-live-restore-target-v1" || !data.ValidID(record.ID) || record.OldTree.Validate() != nil || record.TargetTree.Validate() != nil {
		return &IntegrityError{}
	}
	for _, hash := range []string{record.VerifiedSHA256, record.IntentSHA256, record.RuntimeSHA256, record.AllocationSHA256, record.CheckpointSHA256, record.MaterialSHA256} {
		if !data.ValidCatalogHash(hash) {
			return &IntegrityError{}
		}
	}
	return nil
}
