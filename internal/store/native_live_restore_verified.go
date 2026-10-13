package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

// NativeRestoreObservation crosses only from the fixed trusted host adapter.
// It is not an RPC input or standalone proof. The actual20 claim and an opaque
// authenticated extraction are required before these observations are stored.
type NativeRestoreObservation struct {
	Parent               *os.Root
	TargetTree           data.EngineTreeSeal
	SelectedPointID      string
	SelectedPointSHA256  string
	SourceSHA256         string
	CheckpointSHA256     string
	ManifestSHA256       string
	TransportSHA256      string
	SourceNativeSHA256   string
	Evidence             nativeevidence.Evidence
	RecoveredPairsSHA256 string
	ReaderNonce          string
	ReaderViewSHA256     string
	ReaderImageID        string
	ReaderDisposedAt     time.Time
}

func (NativeRestoreObservation) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

type nativeLiveRestoreVerifiedRecord struct {
	Format               string              `json:"format"`
	ID                   string              `json:"id"`
	Attempt              liverestore.Attempt `json:"attempt"`
	IntentSHA256         string              `json:"intent_sha256"`
	SelectedPointSHA256  string              `json:"selected_point_sha256"`
	SourceSHA256         string              `json:"source_sha256"`
	CheckpointSHA256     string              `json:"checkpoint_sha256"`
	ManifestSHA256       string              `json:"manifest_sha256"`
	TransportSHA256      string              `json:"transport_sha256"`
	SourceNativeSHA256   string              `json:"source_native_sha256"`
	RestoredNativeSHA256 string              `json:"restored_native_sha256"`
	RecoveredPairsSHA256 string              `json:"recovered_pairs_sha256"`
	DependenciesSHA256   string              `json:"dependencies_sha256"`
	TargetTree           data.EngineTreeSeal `json:"target_tree"`
	ReaderNonce          string              `json:"reader_nonce"`
	ReaderViewSHA256     string              `json:"reader_view_sha256"`
	ReaderImageID        string              `json:"reader_image_id"`
	ReaderDisposedAt     time.Time           `json:"reader_disposed_at"`
	VerifiedAt           time.Time           `json:"verified_at"`
}

func (s *Store) RecordNativeVerifiedRestore(ctx context.Context, capability *NativeLiveRestoreExecution, extracted restic.AuthenticatedRestore, observed NativeRestoreObservation) (liverestore.Event, error) {
	if s.readOnly || capability == nil || capability.state != s || !capability.stopped.active(s) {
		return liverestore.Event{}, ErrConflict
	}
	if _, err := s.ReadNativeStoppedPermit(ctx, capability.stopped); err != nil {
		return liverestore.Event{}, err
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return liverestore.Event{}, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, capability.Attempt().OperationID)
	if err != nil || retained.current.SHA256() != capability.IntentSHA256() {
		return liverestore.Event{}, errors.Join(ErrConflict, err)
	}
	if err = checkNativeLiveRestoreExecutionTx(ctx, tx, capability.stopped, retained); err != nil {
		return liverestore.Event{}, err
	}
	bundle, err := readNativeVerifiedPointBundleTx(ctx, tx, retained.claim.Attempt().SelectedPointID)
	if err != nil {
		return liverestore.Event{}, err
	}
	schema, _, err := readNativeVerifiedDependencies(ctx, tx, bundle.point, bundle.checkpoint)
	if err != nil {
		return liverestore.Event{}, err
	}
	if err = checkNativeRestoreObservation(ctx, capability, extracted, bundle, schema, observed); err != nil {
		return liverestore.Event{}, err
	}
	id, err := data.NewID()
	if err != nil {
		return liverestore.Event{}, err
	}
	v := bundle.point.view
	record := nativeLiveRestoreVerifiedRecord{Format: "brine-native-live-restore-verified-v1", ID: id, Attempt: retained.claim.Attempt(), IntentSHA256: retained.current.SHA256(), SelectedPointSHA256: bundle.point.hash, SourceSHA256: v.SourceSHA256, CheckpointSHA256: v.CheckpointSHA256, ManifestSHA256: v.ManifestSHA256, TransportSHA256: v.TransportSHA256, SourceNativeSHA256: v.SourceEvidenceSHA256, RestoredNativeSHA256: observed.Evidence.SHA256(), RecoveredPairsSHA256: observed.RecoveredPairsSHA256, DependenciesSHA256: bundle.dependencies.hash, TargetTree: observed.TargetTree, ReaderNonce: observed.ReaderNonce, ReaderViewSHA256: observed.ReaderViewSHA256, ReaderImageID: observed.ReaderImageID, ReaderDisposedAt: observed.ReaderDisposedAt, VerifiedAt: time.Now().UTC()}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 65536 {
		return liverestore.Event{}, ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_live_restore_target_proofs VALUES(?,?,?)", id, record.Attempt.OperationID, raw)
	if err != nil {
		return liverestore.Event{}, err
	}
	next, err := liverestore.Settle(retained.plan, retained.current, liverestore.Verified, nativePointHash(raw))
	if err != nil {
		return liverestore.Event{}, ErrConflict
	}
	if err = s.appendNativeLiveRestoreEventTx(ctx, tx, capability.stopped.preparation.preparation, retained.current, next); err != nil {
		return liverestore.Event{}, err
	}
	if !extracted.Matches(ctx, observed.Parent, "download", bundle.transport, bundle.manifest) || checkNativeRestoreAnchors(ctx, capability, extracted.Root(), observed) != nil || !capability.stopped.active(s) {
		return liverestore.Event{}, ErrConflict
	}
	return next, tx.Commit()
}

func checkNativeRestoreObservation(ctx context.Context, capability *NativeLiveRestoreExecution, extracted restic.AuthenticatedRestore, bundle nativeVerifiedPointBundle, schema spacetimedb.CanonicalSchema, observed NativeRestoreObservation) error {
	v := bundle.point.view
	if observed.SelectedPointID != v.PointID || observed.SelectedPointSHA256 != bundle.point.hash || observed.SourceSHA256 != v.SourceSHA256 || observed.CheckpointSHA256 != v.CheckpointSHA256 || observed.ManifestSHA256 != v.ManifestSHA256 || observed.TransportSHA256 != v.TransportSHA256 || observed.SourceNativeSHA256 != v.SourceEvidenceSHA256 || observed.TargetTree.Validate() != nil {
		return ErrConflict
	}
	if !extracted.Matches(ctx, observed.Parent, "download", bundle.transport, bundle.manifest) || checkNativeRestoreAnchors(ctx, capability, extracted.Root(), observed) != nil {
		return ErrConflict
	}
	if !data.ValidID(observed.ReaderNonce) || !data.ValidCatalogHash(observed.ReaderViewSHA256) || !data.ValidCatalogHash(observed.ReaderImageID) || observed.ReaderDisposedAt.IsZero() || observed.ReaderDisposedAt.Location() != time.UTC || observed.ReaderDisposedAt.Before(extracted.VerifiedAt()) || observed.ReaderDisposedAt.After(time.Now().UTC()) {
		return ErrConflict
	}
	evidence, err := nativeevidence.Decode(observed.Evidence.Bytes(), observed.Evidence.RequestedLimits())
	if err != nil || evidence.SHA256() != observed.Evidence.SHA256() || !bundle.sourceEvidence.SamePersistedState(evidence) || !nativeBundleEvidenceMatches(v, bundle.checkpoint, evidence) {
		return ErrConflict
	}
	// Recompute pairs from the complete passive state with the actual registered
	// descriptor. A supplied pair hash cannot substitute for native equality.
	pairs, err := spacetimedb.RecoveryPairsFromPassive(evidence, schema)
	if err != nil || pairs.SHA256() != observed.RecoveredPairsSHA256 || spacetimedb.CheckRecoveredPairsEqual(bundle.finalPairs, pairs) != nil || spacetimedb.CheckConfirmedLowerBound(bundle.confirmedBound, pairs) != nil {
		return ErrConflict
	}
	return nil
}
