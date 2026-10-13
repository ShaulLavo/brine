package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
)

// HoldNativePreparationFence preserves an unpublished allocation's original
// fence. Initialized preparation owns a newly held fence as before.
func (s *Store) HoldNativePreparationFence(ctx context.Context, binding data.ReplicaBindingID, operation string) (data.QuiescenceFence, error) {
	if s.readOnly {
		return data.QuiescenceFence{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
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
	if err := refusePendingNativePreparation(ctx, tx, binding, operation); err != nil {
		return data.QuiescenceFence{}, err
	}
	permit, err := readEnginePermit(ctx, tx, binding)
	if err != nil || permit.Allocation == nil {
		return data.QuiescenceFence{}, errors.Join(ErrConflict, err)
	}
	if permit.Runtime.State == data.EngineInitialized {
		if err := tx.Commit(); err != nil {
			return data.QuiescenceFence{}, err
		}
		return s.HoldDataFence(ctx, permit.Reserved.Database.DatabaseID, operation)
	}
	authority, err := readUnpublishedPreparationAuthority(ctx, tx, binding, permit.Reserved.Database, permit.Runtime)
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	var held []data.QuiescenceFence
	for _, fence := range permit.Fences {
		if fence.State == data.FenceHeld {
			held = append(held, fence)
		}
	}
	if len(held) != 1 || held[0].OperationID != authority.operation || held[0].ID != authority.fence {
		return data.QuiescenceFence{}, ErrConflict
	}
	return held[0], tx.Commit()
}

type unpublishedPreparationAuthority struct {
	operation    string
	fence        data.FenceID
	policySHA256 string
}

func readUnpublishedPreparationAuthority(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID, database data.SpacetimeDBBinding, runtime data.EngineRuntime) (unpublishedPreparationAuthority, error) {
	prepared, err := readManagedPreparation(ctx, q, binding)
	if err != nil || runtime.State != data.EngineUnpublished {
		return unpublishedPreparationAuthority{}, errors.Join(ErrConflict, err)
	}
	op, err := nativeResolutionOperation(ctx, q, prepared.OperationID)
	if err != nil || op.Kind != ops.Deploy || op.State != ops.Succeeded || op.PlanID != prepared.PlanID {
		return unpublishedPreparationAuthority{}, errors.Join(ErrConflict, err)
	}
	p, d, err := loadPlan(ctx, q, prepared.PlanID)
	if err != nil || p.Managed == nil || p.Managed.State != data.EngineUnpublished || p.Managed.Database != database || p.App != op.App || !managedAppOwns(ctx, q, op.App, database.IncarnationID) {
		return unpublishedPreparationAuthority{}, errors.Join(ErrConflict, err)
	}
	var id string
	var initialCount int
	err = q.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(MIN(i.operation_id),'') FROM data_native_material_inputs i JOIN operations o ON o.id=i.operation_id WHERE i.binding_id=? AND json_extract(i.canonical,'$.initial')=1 AND o.kind='native_material_set' AND o.state='succeeded'", binding).Scan(&initialCount, &id)
	if err != nil || initialCount != 1 {
		return unpublishedPreparationAuthority{}, errors.Join(ErrConflict, err)
	}
	material, err := readNativeMaterial(ctx, q, id)
	materialRaw, _ := json.Marshal(material.Runtime)
	runtimeRaw, _ := json.Marshal(runtime)
	if err != nil || !material.Initial || material.PreparationOperationID != op.ID || material.Receipt.PreparationPlanID != p.Hash || material.PolicyHash != d.PolicyHash || material.Receipt.Outcome != "configured_unpublished" || !bytes.Equal(materialRaw, runtimeRaw) {
		return unpublishedPreparationAuthority{}, errors.Join(ErrConflict, err)
	}
	materialOp, err := nativeResolutionOperation(ctx, q, id)
	if err != nil || materialOp.App != op.App || materialOp.SecretRef != p.Hash || materialOp.Requester != op.Requester || material.Requester != op.Requester || material.Receipt.App != op.App {
		return unpublishedPreparationAuthority{}, errors.Join(ErrConflict, err)
	}
	var receiptRaw []byte
	err = q.QueryRowContext(ctx, "SELECT canonical FROM data_native_material_receipts WHERE operation_id=?", id).Scan(&receiptRaw)
	expected, _ := json.Marshal(material.Receipt)
	if err != nil || !bytes.Equal(receiptRaw, expected) {
		return unpublishedPreparationAuthority{}, errors.Join(ErrConflict, err)
	}
	var fenceOwner string
	err = q.QueryRowContext(ctx, "SELECT operation_id FROM data_fences WHERE id=? AND database_id=? AND incarnation_id=?", material.FenceID, database.DatabaseID, database.IncarnationID).Scan(&fenceOwner)
	if err != nil || fenceOwner != op.ID {
		return unpublishedPreparationAuthority{}, errors.Join(ErrConflict, err)
	}
	return unpublishedPreparationAuthority{operation: op.ID, fence: material.FenceID, policySHA256: strings.TrimPrefix(d.PolicyHash, "sha256:")}, nil
}

// The immutable intent already binds the fence ID. Its protected owner is
// derived on every read without changing existing canonical journal bytes.
func nativePreparationFenceOwner(ctx context.Context, q dataQuerier, i nativePreparationIntent) (string, error) {
	var owner string
	err := q.QueryRowContext(ctx, "SELECT operation_id FROM data_fences WHERE id=? AND database_id=? AND incarnation_id=?", i.FenceID, i.DatabaseID, i.IncarnationID).Scan(&owner)
	if err != nil {
		return "", errors.Join(ErrConflict, err)
	}
	if owner == i.OperationID {
		return owner, nil
	}
	if i.SourceKind != string(data.EngineUnpublished) {
		return "", ErrConflict
	}
	reserved, err := readReservedEngine(ctx, q, i.BindingID)
	if err != nil || reserved.Database.DatabaseID != i.DatabaseID || reserved.Database.IncarnationID != i.IncarnationID {
		return "", errors.Join(ErrConflict, err)
	}
	var runtimeRaw []byte
	err = q.QueryRowContext(ctx, "SELECT canonical FROM data_engine_runtime_commits WHERE binding_id=? AND version=?", i.BindingID, i.RuntimeVersion).Scan(&runtimeRaw)
	var runtime data.EngineRuntime
	if err != nil || nativePointHash(runtimeRaw) != i.RuntimeSHA256 || json.Unmarshal(runtimeRaw, &runtime) != nil {
		return "", errors.Join(ErrConflict, err)
	}
	proven, err := readUnpublishedPreparationAuthority(ctx, q, i.BindingID, reserved.Database, runtime)
	if err != nil || proven.operation != owner || proven.fence != i.FenceID || proven.policySHA256 != i.PolicySHA256 {
		return "", errors.Join(ErrConflict, err)
	}
	allocation, err := readInitialEngineAllocation(ctx, q, reserved)
	raw, _ := json.Marshal(allocation)
	if err != nil || nativePointHash(raw) != i.AllocationSHA256 {
		return "", errors.Join(ErrConflict, err)
	}
	return owner, nil
}

func refusePendingNativePreparation(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID, operation string) error {
	rows, err := q.QueryContext(ctx, "SELECT o.id FROM data_native_preparations p JOIN operations o ON o.id=p.operation_id WHERE p.binding_id=? AND o.id<>? AND o.state<>'succeeded'", binding, operation)
	if err != nil {
		return err
	}
	pending := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return errors.Join(err, rows.Close())
		}
		pending = append(pending, id)
		if len(pending) > 128 {
			return errors.Join(ErrConflict, rows.Close())
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, id := range pending {
		original, err := nativeResolutionOperation(ctx, q, id)
		if err != nil || original.Kind != ops.NativePrepare || original.State != ops.RecoveryRequired {
			return errors.Join(ErrConflict, err)
		}
		if _, err := readNativeBackupResolvedReceipt(ctx, q, original); err != nil {
			return errors.Join(ErrConflict, err)
		}
	}
	return nil
}
