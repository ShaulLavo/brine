package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type NativePreparedRoutes struct {
	PointID            string            `json:"point_id"`
	OperationID        string            `json:"operation_id"`
	App                string            `json:"app"`
	PreviousGeneration uint64            `json:"previous_generation"`
	PreviousFiles      map[string]string `json:"previous_files"`
	CurrentGeneration  uint64            `json:"current_generation"`
	CurrentFiles       map[string]string `json:"current_files"`
	RecordedAt         time.Time         `json:"recorded_at"`
}

func (s *Store) RecordNativePreparedRoutes(ctx context.Context, preparation *NativePreparation, observed NativePreparedRoutes, completed bool) error {
	if s.readOnly || !preparation.active(s) || observed.PointID != preparation.PointID() || observed.OperationID != preparation.intent.OperationID || observed.App == "" || observed.PreviousFiles == nil || observed.CurrentFiles == nil {
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
	var app string
	err = tx.QueryRowContext(ctx, "SELECT i.app FROM data_incarnations i JOIN data_databases d ON d.incarnation_id=i.id WHERE d.id=?", preparation.intent.DatabaseID).Scan(&app)
	if err != nil || app != observed.App {
		return ErrConflict
	}
	observed.RecordedAt = time.Now().UTC()
	raw, _ := json.Marshal(observed)
	stage := "intent"
	if completed {
		stage = "withdrawn"
	}
	if completed {
		var intentRaw []byte
		var intent NativePreparedRoutes
		err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_prepared_routes WHERE point_id=? AND stage='intent'", observed.PointID).Scan(&intentRaw)
		if err != nil || json.Unmarshal(intentRaw, &intent) != nil || intent.OperationID != observed.OperationID || intent.App != observed.App || intent.PreviousGeneration != observed.PreviousGeneration {
			return ErrConflict
		}
		a, _ := json.Marshal(intent.PreviousFiles)
		b, _ := json.Marshal(observed.PreviousFiles)
		if !bytes.Equal(a, b) {
			return ErrConflict
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_prepared_routes VALUES(?,?,?)", observed.PointID, stage, raw)
	if err != nil || !preparation.active(s) {
		return errors.Join(ErrConflict, err)
	}
	return tx.Commit()
}
func (s *Store) ReadNativePreparedRoutes(ctx context.Context, pointID string) (NativePreparedRoutes, error) {
	point, err := s.ReadNativePreparedPoint(ctx, pointID)
	if err != nil {
		return NativePreparedRoutes{}, err
	}
	var raw []byte
	var routes NativePreparedRoutes
	err = s.db.QueryRowContext(ctx, "SELECT canonical FROM data_native_prepared_routes WHERE point_id=? AND stage='withdrawn'", pointID).Scan(&raw)
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &routes) != nil {
		return routes, errors.Join(&IntegrityError{}, err)
	}
	canonical, _ := json.Marshal(routes)
	if !bytes.Equal(raw, canonical) || routes.PointID != pointID || routes.OperationID != point.PreparationOperationID || routes.RecordedAt.IsZero() || routes.RecordedAt.Location() != time.UTC {
		return routes, &IntegrityError{}
	}
	return routes, nil
}

func nativeHasHeldFence(fences []data.QuiescenceFence) bool {
	for _, fence := range fences {
		if fence.State == data.FenceHeld {
			return true
		}
	}
	return false
}
