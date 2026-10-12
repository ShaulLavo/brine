package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

type nativeArchiveReplay struct {
	Format           string         `json:"format"`
	ID               string         `json:"id"`
	ArchiveID        data.ArchiveID `json:"archive_id"`
	PointID          string         `json:"point_id"`
	PointSHA256      string         `json:"point_sha256"`
	ManifestSHA256   string         `json:"manifest_sha256"`
	TransportSHA256  string         `json:"transport_sha256"`
	EvidenceSHA256   string         `json:"evidence_sha256"`
	ReaderNonce      string         `json:"reader_nonce"`
	ReaderViewSHA256 string         `json:"reader_view_sha256"`
	ReaderImageID    string         `json:"reader_image_id"`
	ReaderDisposedAt time.Time      `json:"reader_disposed_at"`
	VerifiedAt       time.Time      `json:"verified_at"`
}

func (s *Store) RecordNativeArchivedReplay(ctx context.Context, capability *NativeArchivedSource, extracted restic.AuthenticatedRestore, observed NativeArchiveVerificationObservation) (archive.NativeRetentionReceipt, error) {
	var zero archive.NativeRetentionReceipt
	if !capability.active() || capability.state != s || observed.Parent == nil {
		return zero, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	pointID, err := checkNativeArchivedSourceTx(ctx, tx, capability)
	if err != nil {
		return zero, err
	}
	bundle, err := readNativePreparedBundleTx(ctx, tx, pointID)
	if err != nil || !extracted.Matches(ctx, observed.Parent, "download", bundle.Transport(), bundle.Manifest()) {
		return zero, errors.Join(ErrConflict, err)
	}
	if err = checkNativeArchiveReplayEvidence(bundle, extracted, observed); err != nil {
		return zero, err
	}
	id, err := data.NewID()
	if err != nil {
		return zero, err
	}
	point := bundle.Point()
	record := nativeArchiveReplay{Format: "brine-native-archive-replay-v1", ID: id, ArchiveID: capability.archiveID, PointID: pointID, PointSHA256: point.PointSHA256, ManifestSHA256: bundle.Manifest().SHA256(), TransportSHA256: point.TransportPointSHA256, EvidenceSHA256: observed.Evidence.SHA256(), ReaderNonce: observed.ReaderNonce, ReaderViewSHA256: observed.ReaderViewSHA256, ReaderImageID: observed.ReaderImageID, ReaderDisposedAt: observed.ReaderDisposedAt, VerifiedAt: time.Now().UTC()}
	raw, err := json.Marshal(record)
	if err != nil {
		return zero, ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_archive_replays VALUES(?,?,?)", id, capability.archiveID, raw); err != nil {
		return zero, err
	}
	if !capability.active() || !extracted.Matches(ctx, observed.Parent, "download", bundle.Transport(), bundle.Manifest()) {
		return zero, ErrConflict
	}
	return archive.NativeRetentionReceipt{ID: id, SHA256: nativePointHash(raw), ArchiveID: capability.archiveID, BindingID: point.BindingID, EpochID: point.EpochID, PointID: pointID, PointSHA256: point.PointSHA256, VerifiedAt: record.VerifiedAt}, tx.Commit()
}

func checkNativeArchiveReplayEvidence(bundle NativePreparedBundle, extracted restic.AuthenticatedRestore, observed NativeArchiveVerificationObservation) error {
	if bundle.Source().Kind() == "unpublished" {
		_, valid := bundle.Source().Empty()
		if !valid || len(observed.Evidence.Bytes()) != 0 || observed.ReaderNonce != "" || observed.ReaderViewSHA256 != "" || observed.ReaderImageID != "" || !observed.ReaderDisposedAt.IsZero() {
			return ErrConflict
		}
		return nil
	}
	if !data.ValidID(observed.ReaderNonce) || !data.ValidCatalogHash(observed.ReaderViewSHA256) || !data.ValidCatalogHash(observed.ReaderImageID) || observed.ReaderDisposedAt.IsZero() || observed.ReaderDisposedAt.Before(extracted.VerifiedAt()) || observed.ReaderDisposedAt.After(time.Now().UTC()) {
		return ErrConflict
	}
	pairs, err := spacetimedb.RecoveryPairsFromPassive(observed.Evidence, bundle.Schema())
	if err != nil || !bundle.SourceEvidence().SamePersistedState(observed.Evidence) || spacetimedb.CheckConfirmedLowerBound(bundle.ConfirmedBound(), pairs) != nil || spacetimedb.CheckRecoveredPairsEqual(bundle.FinalPairs(), pairs) != nil {
		return ErrConflict
	}
	return nil
}

func checkNativeRetentionReceipts(ctx context.Context, q *sql.Tx, proof archive.RetentionProof) error {
	for _, receipt := range proof.NativeReceipts {
		if err := checkNativeRetentionReceipt(ctx, q, receipt, proof.ArchiveID); err != nil {
			return err
		}
	}
	return nil
}

func checkNativeRetentionReceipt(ctx context.Context, q *sql.Tx, receipt archive.NativeRetentionReceipt, id data.ArchiveID) error {
	var raw []byte
	var retainedArchive data.ArchiveID
	err := q.QueryRowContext(ctx, "SELECT archive_id,canonical FROM data_native_archive_replays WHERE id=?", receipt.ID).Scan(&retainedArchive, &raw)
	var record nativeArchiveReplay
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &record) != nil || record.Format != "brine-native-archive-replay-v1" || record.ID != receipt.ID || record.ArchiveID != id || retainedArchive != id || nativePointHash(raw) != receipt.SHA256 {
		return errors.Join(&IntegrityError{}, err)
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(raw, canonical) {
		return &IntegrityError{}
	}
	point, err := readNativePreparedPointTx(ctx, q, record.PointID)
	if err != nil || point.hash != record.PointSHA256 || point.view.BundleManifestSHA256 != record.ManifestSHA256 || point.view.TransportPointSHA256 != record.TransportSHA256 {
		return errors.Join(&IntegrityError{}, err)
	}
	expected := archive.NativeRetentionReceipt{ID: record.ID, SHA256: nativePointHash(raw), ArchiveID: id, BindingID: point.view.BindingID, EpochID: point.view.EpochID, PointID: record.PointID, PointSHA256: point.hash, VerifiedAt: record.VerifiedAt}
	if !reflect.DeepEqual(expected, receipt) {
		return ErrConflict
	}
	return nil
}
