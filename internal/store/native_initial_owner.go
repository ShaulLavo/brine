package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

func (s *Store) RecordNativeInitialOwner(ctx context.Context, stopped *NativeStoppedPreparation, observed NativeInitialOwnerObservation) error {
	if s.readOnly || !stopped.active(s) || stopped.completedPointSHA256 != "" {
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
	if err := checkNativeInitialOwner(ctx, tx, stopped, observed); err != nil {
		return err
	}
	raw, err := json.Marshal(observed)
	if err != nil || len(raw) > 65536 {
		return ErrInvalid
	}
	var existing []byte
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_initial_owners WHERE point_id=?", stopped.PointID()).Scan(&existing)
	if err == nil {
		if !bytes.Equal(existing, raw) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_initial_owners VALUES(?,?)", stopped.PointID(), raw)
	if err != nil || !stopped.active(s) {
		return errors.Join(ErrConflict, err)
	}
	return tx.Commit()
}

func checkNativeInitialOwner(ctx context.Context, q *sql.Tx, stopped *NativeStoppedPreparation, v NativeInitialOwnerObservation) error {
	permit, err := readEnginePermit(ctx, q, stopped.preparation.intent.BindingID)
	if err != nil || permit.Runtime.State != data.EngineUnpublished || permit.Allocation == nil {
		return errors.Join(ErrConflict, err)
	}
	actual, _ := json.Marshal(permit.Runtime)
	runtime, _ := json.Marshal(v.Runtime)
	if v.Format != "brine-native-initial-owner-provenance-v1" || v.PointID != stopped.PointID() || v.OperationID != stopped.OperationID() || v.BindingID != permit.Runtime.BindingID || v.EpochID != permit.Runtime.EpochID || v.AllocationID != permit.Reserved.Database.DatabaseID || !bytes.Equal(actual, runtime) || !data.ValidID(v.GenerationID) || !data.ValidCatalogHash(v.SourceSHA256) || !data.ValidCatalogHash(v.SourceSealSHA256) || v.StageDevice == 0 || v.StageInode == 0 || v.StageDevice != permit.Allocation.DirectoryDevice || v.HelperPID <= 1 || v.HelperStartTicks == 0 || !strings.HasPrefix(v.HelperCgroup, "/") || len(v.HelperCgroup) > 4096 || v.ChildPID <= 1 || v.ChildStartTicks == 0 || !v.ChildBound || !v.ChildStopped || !v.HelperDisposed || !data.ValidCatalogHash(v.Publisher) || v.CredentialSHA256 != permit.Runtime.Secrets.Publisher.SHA256 || v.PublisherProofSHA256 != nativePointHash(v.PublisherProof) || len(v.PublisherProof) == 0 || len(v.PublisherProof) > 8192 || v.StartedAt.IsZero() || v.StartedAt.Location() != time.UTC || v.FinishedAt.Location() != time.UTC || v.FinishedAt.Before(v.StartedAt) || v.StartedAt.Before(stopped.preparation.intent.CreatedAt) || v.FinishedAt.After(time.Now().UTC()) {
		return ErrConflict
	}
	return checkNativeInitialPublisherTranscript(v)
}

func checkNativeInitialPublisherTranscript(v NativeInitialOwnerObservation) error {
	fields, err := strictjson.Object(v.PublisherProof, "purpose", "endpoint_sha256", "method", "path", "status", "publisher", "credential_sha256", "verified_at")
	if err != nil {
		return ErrInvalid
	}
	var proof struct {
		Purpose    string    `json:"purpose"`
		Endpoint   string    `json:"endpoint_sha256"`
		Method     string    `json:"method"`
		Path       string    `json:"path"`
		Status     int       `json:"status"`
		Publisher  string    `json:"publisher"`
		Credential string    `json:"credential_sha256"`
		VerifiedAt time.Time `json:"verified_at"`
	}
	endpoint, err := spacetimedb.NewPrivateServerEndpoint("127.0.0.1", 23991)
	if err != nil || json.Unmarshal(v.PublisherProof, &proof) != nil || proof.Purpose != "native_supplied_key_publisher_verification" || proof.Endpoint != endpoint.SHA256() || proof.Method != "GET" || proof.Path != "/v1/identity/"+v.Publisher+"/verify" || proof.Status != 204 || proof.Publisher != v.Publisher || proof.Credential != v.CredentialSHA256 || proof.VerifiedAt.Location() != time.UTC || proof.VerifiedAt.Before(v.StartedAt) || proof.VerifiedAt.After(v.FinishedAt) {
		return ErrConflict
	}
	stamp, err := strictjson.Value[string](fields["verified_at"])
	if err != nil || !strings.HasSuffix(stamp, "Z") {
		return ErrConflict
	}
	return nil
}

func (s *Store) ReadNativeInitialOwner(ctx context.Context, stopped *NativeStoppedPreparation) (NativeInitialOwnerObservation, error) {
	if !stopped.active(s) {
		return NativeInitialOwnerObservation{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeInitialOwnerObservation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkNativeStoppedCapability(ctx, tx, stopped); err != nil {
		return NativeInitialOwnerObservation{}, err
	}
	v, raw, err := readNativeInitialOwner(ctx, tx, stopped)
	if err != nil {
		return v, err
	}
	if !stopped.active(s) {
		return v, ErrConflict
	}
	if stopped.completedPointSHA256 != "" {
		point, err := readNativePreparedPointTx(ctx, tx, stopped.PointID())
		if err != nil || v.SourceSHA256 != point.view.SourceSHA256 || v.SourceSealSHA256 != point.view.SourceSealSHA256 || v.GenerationID != point.view.GenerationID || nativePointHash(raw) != point.view.SourceWitnessSHA256 {
			return v, errors.Join(&IntegrityError{}, err)
		}
	}
	return v, tx.Commit()
}

func readNativeInitialOwner(ctx context.Context, tx *sql.Tx, stopped *NativeStoppedPreparation) (NativeInitialOwnerObservation, []byte, error) {
	var raw []byte
	var v NativeInitialOwnerObservation
	err := tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_initial_owners WHERE point_id=?", stopped.PointID()).Scan(&raw)
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &v) != nil {
		return v, nil, errors.Join(&IntegrityError{}, err)
	}
	canonical, _ := json.Marshal(v)
	if !bytes.Equal(raw, canonical) || checkNativeInitialOwner(ctx, tx, stopped, v) != nil {
		return v, nil, &IntegrityError{}
	}
	return v, raw, nil
}
