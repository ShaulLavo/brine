package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/data"
	"time"
)

func (s *Store) RecordNativePreparedFrontendStop(ctx context.Context, preparation *NativePreparation, stopped bool) error {
	if s.readOnly || !preparation.active(s) {
		return ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkNativePreparationCapability(ctx, tx, preparation); err != nil {
		return err
	}
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", preparation.intent.OperationID))
	if err != nil {
		return err
	}
	var releaseID string
	err = tx.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", op.App).Scan(&releaseID)
	if err != nil {
		return err
	}
	release, err := readRelease(ctx, tx, op.App, releaseID)
	if err != nil {
		return err
	}
	if checkNativePreparedRelease(ctx, tx, preparation.intent, release) != nil || release.Managed.Frontend == nil {
		return ErrConflict
	}
	stage := "intent"
	if stopped {
		stage = "stopped"
	}
	if stopped {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM data_native_prepared_frontend_stops WHERE point_id=? AND stage='intent'", preparation.PointID()).Scan(&count); err != nil || count != 1 {
			return ErrConflict
		}
	}
	raw, _ := json.Marshal(struct {
		PointID     string    `json:"point_id"`
		OperationID string    `json:"operation_id"`
		ReleaseID   string    `json:"release_id"`
		Unit        string    `json:"unit"`
		UnitSHA     string    `json:"unit_sha256"`
		At          time.Time `json:"observed_at"`
	}{preparation.PointID(), preparation.intent.OperationID, release.ID, release.Managed.Frontend.Unit.Name, release.Managed.Frontend.Unit.Hash, time.Now().UTC()})
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_prepared_frontend_stops VALUES(?,?,?)", preparation.PointID(), stage, raw)
	if err != nil || !preparation.active(s) {
		return errors.Join(ErrConflict, err)
	}
	return tx.Commit()
}

func checkNativePreparedRelease(ctx context.Context, q dataQuerier, intent nativePreparationIntent, release Release) error {
	if release.Managed == nil || release.Managed.BindingID != intent.BindingID || release.Managed.EpochID != intent.EpochID {
		return ErrConflict
	}
	head := data.EngineHeadRef{BindingID: intent.BindingID, EpochID: intent.EpochID, Version: intent.RuntimeVersion, RuntimeSHA256: intent.RuntimeSHA256}
	ancestor, err := materialRuntimeHead(ctx, q, head, release.Managed.RuntimeSHA256)
	if err != nil {
		return err
	}
	if ancestor.RuntimeSHA256 != release.Managed.RuntimeSHA256 {
		return ErrConflict
	}
	return nil
}
