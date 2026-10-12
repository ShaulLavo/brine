package store

import (
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/restic"
	"os"
	"time"
)

type NativeArchiveVerificationObservation struct {
	Parent            *os.Root
	Evidence          nativeevidence.Evidence
	ReaderNonce       string
	ReaderViewSHA256  string
	ReaderImageID     string
	ReaderDisposedAt  time.Time
	WritersQuiescedAt time.Time
}

func (NativeArchiveVerificationObservation) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

type nativeArchiveVerification struct {
	Format           string                 `json:"format"`
	ArchiveID        data.ArchiveID         `json:"archive_id"`
	PointID          string                 `json:"point_id"`
	PointSHA256      string                 `json:"point_sha256"`
	SourceSHA256     string                 `json:"source_sha256"`
	NativeSHA256     string                 `json:"native_sha256"`
	ManifestSHA256   string                 `json:"manifest_sha256"`
	TransportSHA256  string                 `json:"transport_sha256"`
	ReaderNonce      string                 `json:"reader_nonce"`
	ReaderViewSHA256 string                 `json:"reader_view_sha256"`
	ReaderImageID    string                 `json:"reader_image_id"`
	ReaderDisposedAt time.Time              `json:"reader_disposed_at"`
	Coverage         archive.NativeCoverage `json:"coverage"`
}

func (s *Store) RecordNativeArchiveVerified(ctx context.Context, id data.ArchiveID, stopped *NativeStoppedPreparation, extracted restic.AuthenticatedRestore, observed NativeArchiveVerificationObservation) (archive.Coverage, error) {
	var zero archive.Coverage
	if !stopped.active(s) || observed.Parent == nil {
		return zero, ErrConflict
	}
	if _, err := s.ReadNativeStoppedPermit(ctx, stopped); err != nil {
		return zero, err
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := loadArchivePlanByArchive(ctx, tx, id)
	if err != nil {
		return zero, err
	}
	o, exists, err := readArchiveOperation(ctx, tx, p.ID)
	if err != nil || !exists || o.Stage != archive.CoverageIntent {
		return zero, ErrConflict
	}
	point, err := readNativePreparedPointTx(ctx, tx, stopped.PointID())
	if err != nil || point.view.StopProofSHA256 != stopped.stopSHA256 || point.view.FenceOwnerOpID != stopped.OperationID() || checkNativePointCurrent(ctx, tx, point.view) != nil {
		return zero, ErrConflict
	}
	if checkNativeArchiveFenceOwner(ctx, tx, o, data.QuiescenceFence{DatabaseID: point.view.DatabaseID, OperationID: point.view.FenceOwnerOpID}) != nil {
		return zero, ErrConflict
	}
	bundle, err := readNativePreparedBundleTx(ctx, tx, point.view.PointID)
	if err != nil || checkNativeArchiveReplayEvidence(bundle, extracted, observed) != nil || !extracted.Matches(ctx, observed.Parent, "download", bundle.Transport(), bundle.Manifest()) {
		return zero, ErrConflict
	}
	head, _, err := readEngineHead(ctx, tx, point.view.BindingID)
	if err != nil {
		return zero, err
	}
	now := time.Now().UTC()
	verifiedAt := observed.ReaderDisposedAt
	if bundle.Source().Kind() == "unpublished" {
		verifiedAt = extracted.VerifiedAt()
	}
	if observed.WritersQuiescedAt.Before(p.CreatedAt) || observed.WritersQuiescedAt.After(verifiedAt) {
		return zero, ErrConflict
	}
	v := point.view
	coverage := archive.NativeCoverage{DatabaseID: v.DatabaseID, Head: head, Point: data.EngineEvidenceRef{Kind: data.EnginePreparedPointEvidence, ID: v.PointID, SHA256: point.hash, DatabaseID: v.DatabaseID, BindingID: v.BindingID, EpochID: v.EpochID}, FenceID: v.FenceID, FenceOwnerOpID: v.FenceOwnerOpID, SourceWitnessSHA256: v.SourceWitnessSHA256, CheckpointSHA256: v.CheckpointSHA256, RestoreReceiptSHA256: v.RestoreReceiptSHA256, StopProofSHA256: v.StopProofSHA256, WritersQuiescedAt: observed.WritersQuiescedAt, VerifiedAt: now, ExpiresAt: v.ExpiresAt}
	record := nativeArchiveVerification{Format: "brine-native-archive-verification-v1", ArchiveID: id, PointID: v.PointID, PointSHA256: point.hash, SourceSHA256: bundle.Source().SHA256(), NativeSHA256: observed.Evidence.SHA256(), ManifestSHA256: bundle.Manifest().SHA256(), TransportSHA256: v.TransportPointSHA256, ReaderNonce: observed.ReaderNonce, ReaderViewSHA256: observed.ReaderViewSHA256, ReaderImageID: observed.ReaderImageID, ReaderDisposedAt: observed.ReaderDisposedAt, Coverage: coverage}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 65536 {
		return zero, ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_archive_verifications VALUES(?,?,?)", id, v.PointID, raw); err != nil {
		return zero, err
	}
	if !stopped.active(s) || !extracted.Matches(ctx, observed.Parent, "download", bundle.Transport(), bundle.Manifest()) {
		return zero, ErrConflict
	}
	return archive.NativeCoverageVariant(coverage), tx.Commit()
}
