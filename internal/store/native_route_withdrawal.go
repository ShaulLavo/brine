package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
)

// NativeRouteWithdrawal reads a completed native preparation's deliberate
// ingress withdrawal. The caller must match both complete manifests to its
// committed expectations and observed generation before subtracting the site.
func (s *Store) NativeRouteWithdrawal(ctx context.Context, head ManagedRouteState, policyHash string, generation uint64) (NativePreparedRoutes, error) {
	if !head.Valid() || !head.Present || generation <= head.Generation {
		return NativePreparedRoutes{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativePreparedRoutes{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := readManagedRouteState(ctx, tx, head.App)
	if err != nil || current != head {
		return NativePreparedRoutes{}, errors.Join(ErrConflict, err)
	}
	var point sql.NullString
	var matches int
	err = tx.QueryRowContext(ctx, `SELECT min(r.point_id),count(*) FROM data_native_prepared_routes r
 JOIN data_native_prepared_points p ON p.id=r.point_id
 WHERE r.stage='withdrawn' AND p.binding_id=? AND p.epoch_id=?
 AND json_extract(r.canonical,'$.app')=? AND json_extract(r.canonical,'$.current_generation')=?
 AND json_extract(r.canonical,'$.previous_generation')=?`, head.BindingID, head.EpochID, head.App, generation, head.Generation).Scan(&point, &matches)
	if err == nil && matches == 0 {
		return NativePreparedRoutes{}, ErrNotFound
	}
	if err != nil {
		return NativePreparedRoutes{}, err
	}
	if matches != 1 || !point.Valid {
		return NativePreparedRoutes{}, ErrConflict
	}
	pointID := point.String
	prepared, err := readNativePreparedPointTx(ctx, tx, pointID)
	if err != nil {
		return NativePreparedRoutes{}, err
	}
	op, err := nativeResolutionOperation(ctx, tx, prepared.view.PreparationOperationID)
	if err != nil || op.Kind != ops.NativePrepare || op.App != head.App {
		return NativePreparedRoutes{}, errors.Join(ErrConflict, err)
	}
	input, err := readNativeTaskInput(ctx, tx, op)
	if err != nil || input.Kind != ops.NativePrepare || input.BindingID != head.BindingID || input.Requester.String() != op.Requester {
		return NativePreparedRoutes{}, errors.Join(ErrConflict, err)
	}
	if err := nativeRouteWithdrawalCurrent(ctx, tx, head, policyHash, prepared.view); err != nil {
		return NativePreparedRoutes{}, err
	}
	var raw []byte
	var routes NativePreparedRoutes
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_prepared_routes WHERE point_id=? AND stage='withdrawn'", pointID).Scan(&raw)
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &routes) != nil {
		return NativePreparedRoutes{}, errors.Join(&IntegrityError{}, err)
	}
	canonical, err := json.Marshal(routes)
	if err != nil || !bytes.Equal(raw, canonical) || routes.PointID != pointID || routes.OperationID != op.ID || routes.App != head.App || routes.PreviousGeneration != head.Generation || routes.CurrentGeneration != generation || routes.RecordedAt.IsZero() || routes.RecordedAt.Location() != time.UTC || routes.PreviousFiles[head.App+".caddy"] != head.FileHash {
		return NativePreparedRoutes{}, ErrConflict
	}
	wanted := maps.Clone(routes.PreviousFiles)
	delete(wanted, head.App+".caddy")
	if !maps.Equal(wanted, routes.CurrentFiles) || routes.CurrentFiles == nil {
		return NativePreparedRoutes{}, ErrConflict
	}
	if err := nativeRouteWithdrawalFence(ctx, tx, prepared.view); err != nil {
		return NativePreparedRoutes{}, err
	}
	return routes, tx.Commit()
}

func nativeRouteWithdrawalFence(ctx context.Context, tx *sql.Tx, point NativePreparedPointView) error {
	var state data.FenceState
	err := tx.QueryRowContext(ctx, "SELECT state FROM data_fences WHERE id=? AND operation_id=? AND database_id=? AND incarnation_id=?", point.FenceID, point.FenceOwnerOpID, point.DatabaseID, point.IncarnationID).Scan(&state)
	if err != nil {
		return err
	}
	if state == data.FenceHeld {
		return nil
	}
	var abandonment string
	err = tx.QueryRowContext(ctx, `SELECT o.id FROM operations o JOIN operation_outcomes r ON r.operation_id=o.id
 WHERE o.kind='native_prepare_abandon' AND o.state='succeeded'
 AND json_extract(r.canonical,'$.receipt.point_id')=? LIMIT 1`, point.PointID).Scan(&abandonment)
	if err != nil || state != data.FenceReleased {
		return errors.Join(ErrConflict, err)
	}
	_, err = readNativeAbandonmentOutcomeTx(ctx, tx, abandonment)
	return err
}

func nativeRouteWithdrawalCurrent(ctx context.Context, tx *sql.Tx, head ManagedRouteState, policyHash string, point NativePreparedPointView) error {
	permit, err := readEnginePermit(ctx, tx, head.BindingID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(permit.Runtime)
	if err != nil || point.BindingID != head.BindingID || point.EpochID != head.EpochID || point.RuntimeSHA256 != head.RuntimeSHA256 || policyHash != "sha256:"+point.PolicySHA256 || permit.Runtime.EpochID != point.EpochID || nativePointHash(raw) != point.RuntimeSHA256 || permit.Reserved.Database.DatabaseID != point.DatabaseID || permit.Reserved.Database.IncarnationID != point.IncarnationID || !managedAppOwns(ctx, tx, head.App, point.IncarnationID) || permit.Allocation == nil {
		return ErrConflict
	}
	allocation, err := json.Marshal(permit.Allocation)
	if err != nil || nativePointHash(allocation) != point.AllocationSHA256 {
		return ErrConflict
	}
	return nil
}
