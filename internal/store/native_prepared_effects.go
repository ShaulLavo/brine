package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/restic"
)

func (s *Store) RecordNativePreparedUploadIntent(ctx context.Context, stopped *NativeStoppedPreparation, source nativecapture.Source, manifest restic.Manifest, retained NativeUploadRetention) error {
	if s.readOnly || !stopped.active(s) {
		return ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkNativeStoppedCapability(ctx, tx, stopped); err != nil {
		return err
	}
	if err := checkNativePreparedSourceRuntime(ctx, tx, stopped, source); err != nil {
		return err
	}
	permit, err := readEnginePermit(ctx, tx, stopped.preparation.intent.BindingID)
	if err != nil {
		return err
	}
	pointID := stopped.PointID()
	if err := retainNativeUpload(ctx, tx, permit, stopped.OperationID(), pointID, source, manifest, retained); err != nil {
		return err
	}
	raw, _ := json.Marshal(struct {
		PointID     string    `json:"point_id"`
		SourceSHA   string    `json:"source_sha256"`
		ManifestSHA string    `json:"manifest_sha256"`
		At          time.Time `json:"issued_at"`
	}{pointID, source.SHA256(), manifest.SHA256(), time.Now().UTC()})
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_prepared_upload_intents VALUES(?,?,?,?)", pointID, source.SHA256(), manifest.SHA256(), raw)
	if err != nil || !stopped.active(s) {
		return errors.Join(ErrConflict, err)
	}
	return tx.Commit()
}
