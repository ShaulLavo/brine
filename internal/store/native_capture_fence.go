package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
)

func holdNativeCaptureFence(ctx context.Context, tx *sql.Tx, c *NativeOnlineCapture) error {
	id, err := data.NewID()
	if err != nil {
		return err
	}
	binding := c.permit.Reserved.Database
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_fences VALUES(?,?,?,?,?)", id, binding.DatabaseID, binding.IncarnationID, c.operationID, data.FenceHeld); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_capture_fences VALUES(?,?,?)", c.operationID, c.permit.Runtime.BindingID, id)
	return err
}

func nativeCapturePermitIdentity(ctx context.Context, q dataQuerier, operation string, current EnginePermit) (EnginePermit, error) {
	var binding data.ReplicaBindingID
	var id data.FenceID
	err := q.QueryRowContext(ctx, "SELECT binding_id,fence_id FROM data_native_capture_fences WHERE operation_id=?", operation).Scan(&binding, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return current, nil
	}
	if err != nil || binding != current.Runtime.BindingID {
		return EnginePermit{}, ErrConflict
	}
	kept := make([]data.QuiescenceFence, 0, len(current.Fences))
	found := 0
	for _, fence := range current.Fences {
		if fence.ID != id {
			kept = append(kept, fence)
			continue
		}
		if fence.OperationID != operation || fence.DatabaseID != current.Reserved.Database.DatabaseID || fence.IncarnationID != current.Reserved.Database.IncarnationID || fence.State != data.FenceHeld {
			return EnginePermit{}, ErrConflict
		}
		found++
	}
	if found != 1 {
		return EnginePermit{}, ErrConflict
	}
	current.Fences = kept
	return current, nil
}

func completeNativeCaptureFence(ctx context.Context, tx *sql.Tx, op ops.Operation, outcome ops.TaskOutcome) error {
	if op.State != ops.Succeeded {
		return nil
	}
	original := op.ID
	if op.Kind == ops.NativeBackupResolve {
		r, err := nativecapture.DecodeBackupResolutionReceipt(outcome.Receipt)
		if err != nil {
			return err
		}
		if r.OriginalKind != string(ops.NativeCapture) {
			return nil
		}
		original = r.OriginalOperationID
		source, err := nativeResolutionOperation(ctx, tx, original)
		if err != nil {
			return err
		}
		if _, err = readNativeBackupResolvedReceipt(ctx, tx, source); err != nil {
			return err
		}
	} else if op.Kind != ops.NativeCapture {
		return nil
	} else if err := checkNativeResolvedProducer(ctx, tx, op, outcome.Receipt); err != nil {
		return err
	}
	var fence data.FenceID
	err := tx.QueryRowContext(ctx, "SELECT fence_id FROM data_native_capture_fences WHERE operation_id=?", original).Scan(&fence)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	rows, err := tx.ExecContext(ctx, "UPDATE data_fences SET state='released' WHERE id=? AND operation_id=? AND state='held'", fence, original)
	if err != nil {
		return err
	}
	count, err := rows.RowsAffected()
	if err != nil || count != 1 {
		return ErrConflict
	}
	return nil
}

func nativeCaptureFenceReceipt(ctx context.Context, q dataQuerier, original string) error {
	op, err := nativeResolutionOperation(ctx, q, original)
	if err != nil {
		return err
	}
	if op.Kind != ops.NativeCapture {
		return ErrConflict
	}
	if op.State == ops.RecoveryRequired {
		_, err = readNativeBackupResolvedReceipt(ctx, q, op)
		return err
	}
	if op.State != ops.Succeeded {
		return ErrConflict
	}
	var raw []byte
	if err = q.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", original).Scan(&raw); err != nil {
		return err
	}
	outcome, err := ops.DecodeTaskOutcome(op, raw)
	if err != nil {
		return err
	}
	receipt, err := nativecapture.DecodeTaskReceipt(outcome.Receipt)
	if err != nil {
		return err
	}
	pointRaw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return checkNativeResolvedProducer(ctx, q, op, pointRaw)
}
