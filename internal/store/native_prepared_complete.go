package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"slices"
)

// NativePreparedObservation comes from the fixed host reader and exact stopped
// source producer. It cannot authorize completion without retained locks and an
// actual authenticated remote extraction of the same archive.
type NativePreparedObservation struct {
	Parent           *os.Root
	Source           nativecapture.Source
	Manifest         restic.Manifest
	Transport        restic.TransportPoint
	SourceSealSHA256 string
	SourceEvidence   nativeevidence.Evidence
	RestoredEvidence nativeevidence.Evidence
	ConfirmedBound   spacetimedb.ConfirmedPairsRecord
	FinalPairs       spacetimedb.RecoveredPairs
	Artifact         []byte
	Descriptor       []byte
	ReaderNonce      string
	ReaderViewSHA256 string
	ReaderImageID    string
	ReaderDisposedAt time.Time
}

func (NativePreparedObservation) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

type nativePreparedVerification struct {
	Format               string    `json:"format"`
	PointID              string    `json:"point_id"`
	SourceSHA256         string    `json:"source_sha256"`
	SourceNativeSHA256   string    `json:"source_native_sha256"`
	RestoredNativeSHA256 string    `json:"restored_native_sha256"`
	FinalPairsSHA256     string    `json:"final_pairs_sha256"`
	ConfirmedBoundSHA256 string    `json:"confirmed_bound_sha256"`
	ManifestSHA256       string    `json:"manifest_sha256"`
	TransportSHA256      string    `json:"transport_sha256"`
	ReaderNonce          string    `json:"reader_nonce"`
	ReaderViewSHA256     string    `json:"reader_view_sha256"`
	ReaderImageID        string    `json:"reader_image_id"`
	ReaderDisposedAt     time.Time `json:"reader_disposed_at"`
	VerifiedAt           time.Time `json:"verified_at"`
}

func (s *Store) CompleteNativePreparedPoint(ctx context.Context, stopped *NativeStoppedPreparation, extracted restic.AuthenticatedRestore, observed NativePreparedObservation) (NativePreparedPointView, error) {
	if s.readOnly || !stopped.active(s) || stopped.completedPointSHA256 != "" {
		return NativePreparedPointView{}, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return NativePreparedPointView{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkNativeStoppedCapability(ctx, tx, stopped); err != nil {
		return NativePreparedPointView{}, err
	}
	if checkNativePreparedSourceRuntime(ctx, tx, stopped, observed.Source) != nil {
		return NativePreparedPointView{}, ErrConflict
	}
	if _, err := readNativeRepositoryMaterial(ctx, tx, observed.Transport); err != nil {
		return NativePreparedPointView{}, err
	}
	var issuedSource, issuedManifest string
	err = tx.QueryRowContext(ctx, "SELECT source_sha256,manifest_sha256 FROM data_native_prepared_upload_intents WHERE point_id=?", stopped.PointID()).Scan(&issuedSource, &issuedManifest)
	if err != nil || issuedSource != observed.Source.SHA256() || issuedManifest != observed.Manifest.SHA256() {
		return NativePreparedPointView{}, ErrConflict
	}
	if observed.Source.Kind() != stopped.preparation.intent.SourceKind || !data.ValidCatalogHash(observed.SourceSealSHA256) || !extracted.Matches(ctx, observed.Parent, "download", observed.Transport, observed.Manifest) {
		return NativePreparedPointView{}, ErrConflict
	}
	sourceRaw, err := json.Marshal(observed.Source)
	transportRaw, transportErr := json.Marshal(observed.Transport)
	if err != nil || transportErr != nil || checkNativeBundleMembers(observed.Source, observed.Manifest, sourceRaw) != nil {
		return NativePreparedPointView{}, ErrInvalid
	}
	i := stopped.preparation.intent
	now := time.Now().UTC()
	v := NativePreparedPointView{Format: nativePreparedPointFormat, PointID: i.PointID, PreparationOperationID: i.OperationID, FenceOwnerOpID: i.OperationID, FenceID: i.FenceID, BindingID: i.BindingID, EpochID: i.EpochID, DatabaseID: i.DatabaseID, IncarnationID: i.IncarnationID, Generation: i.Generation, PolicySHA256: i.PolicySHA256, RuntimeVersion: i.RuntimeVersion, RuntimeSHA256: i.RuntimeSHA256, AllocationID: string(i.DatabaseID), AllocationSHA256: i.AllocationSHA256, SourceKind: i.SourceKind, SourceSHA256: observed.Source.SHA256(), SourceSealSHA256: observed.SourceSealSHA256, StopProofSHA256: stopped.stopSHA256, BundleManifestSHA256: observed.Manifest.SHA256(), TransportPointSHA256: nativePointHash(transportRaw), Names: []string{}, PreparedAt: now, ExpiresAt: now.Add(10 * time.Minute), RetainUntil: now.Add(30 * 24 * time.Hour)}
	var sourceEvidence, restoredEvidence, confirmed, final []byte
	checkpoint, _, initialized := observed.Source.Initialized()
	if initialized {
		c := checkpoint.Record()
		v.GenerationID, v.CheckpointSHA256, v.ConfigSHA256 = c.GenerationID, checkpoint.SHA256(), c.ConfigurationSHA256
		v.NativeDatabase, v.NativePublisher, v.ArtifactWasmSHA256, v.ProgramWireHash, v.SchemaSHA256, v.Names = c.DatabaseIdentity, c.PublisherIdentity, c.ArtifactSHA256, c.RuntimeModuleHash, c.SchemaSHA256, c.Names
		v.CommittedOffset, v.CommittedOffsetKnown, v.SourceWitnessSHA256 = observed.SourceEvidence.Offset(), true, observed.SourceEvidence.SHA256()
		recovery, _ := json.Marshal(c.SigningRecovery)
		v.SigningRecoverySHA256 = nativePointHash(recovery)
		sourceEvidence, restoredEvidence, confirmed, final = observed.SourceEvidence.Bytes(), observed.RestoredEvidence.Bytes(), observed.ConfirmedBound.Bytes(), observed.FinalPairs.Bytes()
		if err := checkNativePreparedReader(observed, extracted); err != nil {
			return NativePreparedPointView{}, err
		}
		id, err := data.NewID()
		if err != nil {
			return NativePreparedPointView{}, err
		}
		provenance, _ := json.Marshal(nativeCheckpointProvenance{Format: "brine-native-checkpoint-provenance-v1", PointID: i.PointID, BindingID: i.BindingID, EpochID: i.EpochID, GenerationID: c.GenerationID, CheckpointSHA256: checkpoint.SHA256(), SourceSHA256: observed.Source.SHA256(), SourceEvidenceSHA256: v.SourceWitnessSHA256, ArtifactSHA256: c.ArtifactSHA256, ProgramWireHash: c.RuntimeModuleHash, DescriptorSHA256: nativePointHash(observed.Descriptor), SchemaSHA256: c.SchemaSHA256, ReaderDisposalSHA256: nativePointHash([]byte(observed.ReaderNonce + observed.ReaderViewSHA256 + observed.ReaderImageID))})
		_, err = tx.ExecContext(ctx, "INSERT INTO data_native_checkpoints VALUES(?,?,?,?,?,?,?,?,?)", id, i.BindingID, i.EpochID, c.GenerationID, i.OperationID, checkpoint.Bytes(), provenance, observed.Artifact, observed.Descriptor)
		if err != nil {
			return NativePreparedPointView{}, err
		}
	} else {
		empty, valid := observed.Source.Empty()
		var ownerRaw []byte
		var owner NativeInitialOwnerObservation
		err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_initial_owners WHERE point_id=?", i.PointID).Scan(&ownerRaw)
		if !valid || err != nil || json.Unmarshal(ownerRaw, &owner) != nil || checkNativeInitialOwner(ctx, tx, stopped, owner) != nil || owner.SourceSHA256 != v.SourceSHA256 || owner.SourceSealSHA256 != v.SourceSealSHA256 || owner.GenerationID != empty.GenerationID {
			return NativePreparedPointView{}, errors.Join(ErrConflict, err)
		}
		v.GenerationID, v.CheckpointSHA256, v.ConfigSHA256, v.SourceWitnessSHA256 = empty.GenerationID, observed.Source.SHA256(), empty.ConfigurationSHA256, nativePointHash(ownerRaw)
		recovery, _ := json.Marshal(empty.SigningRecovery)
		v.SigningRecoverySHA256 = nativePointHash(recovery)
	}
	verification := nativePreparedVerification{Format: "brine-native-prepared-verification-v1", PointID: i.PointID, SourceSHA256: v.SourceSHA256, SourceNativeSHA256: nativePointHash(sourceEvidence), RestoredNativeSHA256: nativePointHash(restoredEvidence), FinalPairsSHA256: nativePointHash(final), ConfirmedBoundSHA256: nativePointHash(confirmed), ManifestSHA256: v.BundleManifestSHA256, TransportSHA256: v.TransportPointSHA256, ReaderNonce: observed.ReaderNonce, ReaderViewSHA256: observed.ReaderViewSHA256, ReaderImageID: observed.ReaderImageID, ReaderDisposedAt: observed.ReaderDisposedAt, VerifiedAt: now}
	verifiedRaw, _ := json.Marshal(verification)
	v.RestoreReceiptSHA256 = nativePointHash(verifiedRaw)
	raw, err := json.Marshal(v)
	if err != nil || !validNativePreparedPoint(v) {
		return NativePreparedPointView{}, ErrInvalid
	}
	point := nativePreparedPoint{view: v, raw: raw, hash: nativePointHash(raw)}
	if _, err := decodeNativePreparedBundle(ctx, tx, point, sourceRaw, observed.Manifest.Bytes(), transportRaw, sourceEvidence, restoredEvidence, confirmed, final); err != nil {
		return NativePreparedPointView{}, err
	}
	sourceFacts, _ := nativePreparedSourceFactBytes(v)
	remoteFacts, _ := nativePreparedRemoteFactBytes(v)
	for _, stage := range []struct {
		name string
		raw  []byte
	}{{"source", sourceFacts}, {"remote", remoteFacts}, {"verified", verifiedRaw}, {"completed", raw}} {
		if err := appendNativePreparationProof(ctx, tx, i, stopped.preparation.raw, stage.name, stage.raw); err != nil {
			return NativePreparedPointView{}, err
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_prepared_points VALUES(?,?,?,?)", i.PointID, i.BindingID, i.EpochID, raw)
	if err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO data_native_prepared_point_bundles VALUES(?,?,?,?,?,?,?,?)", i.PointID, sourceRaw, observed.Manifest.Bytes(), transportRaw, sourceEvidence, restoredEvidence, confirmed, final)
	}
	if err != nil || !stopped.active(s) || !extracted.Matches(ctx, observed.Parent, "download", observed.Transport, observed.Manifest) {
		return NativePreparedPointView{}, errors.Join(ErrConflict, err)
	}
	v.PointSHA256 = point.hash
	return v, tx.Commit()
}

func checkNativePreparedReader(v NativePreparedObservation, extracted restic.AuthenticatedRestore) error {
	if !data.ValidID(v.ReaderNonce) || !data.ValidCatalogHash(v.ReaderViewSHA256) || !data.ValidCatalogHash(v.ReaderImageID) || v.ReaderDisposedAt.IsZero() || v.ReaderDisposedAt.Location() != time.UTC || v.ReaderDisposedAt.Before(extracted.VerifiedAt()) || v.ReaderDisposedAt.After(time.Now().UTC()) {
		return ErrConflict
	}
	return nil
}

func checkNativePreparedSourceRuntime(ctx context.Context, tx *sql.Tx, stopped *NativeStoppedPreparation, source nativecapture.Source) error {
	permit, err := readEnginePermit(ctx, tx, stopped.preparation.intent.BindingID)
	if err != nil {
		return err
	}
	r := permit.Runtime
	if checkpoint, _, initialized := source.Initialized(); initialized {
		c := checkpoint.Record()
		if r.State != data.EngineInitialized || c.Scope.BindingID != string(r.BindingID) || c.Scope.EpochID != string(r.EpochID) || c.DatabaseIdentity != string(r.Database) || c.PublisherIdentity != string(r.Publisher) || c.ArtifactSHA256 != string(r.Module.Wasm) || c.RuntimeModuleHash != string(r.Module.Program) || c.SchemaVersion != r.Module.Schema.Canonicalizer || c.SchemaSHA256 != r.Module.Schema.SHA256 || !slices.Equal(c.Names, r.Names) || !nativeSigningRecoveryMatches(c.SigningRecovery, r.Secrets.SigningPrivate) {
			return ErrConflict
		}
		return nil
	}
	empty, valid := source.Empty()
	if !valid || r.State != data.EngineUnpublished || empty.Scope.BindingID != string(r.BindingID) || empty.Scope.EpochID != string(r.EpochID) || empty.PublisherReference != r.Secrets.Publisher.Reference || !nativeSigningRecoveryMatches(empty.SigningRecovery, r.Secrets.SigningPrivate) {
		return ErrConflict
	}
	return nil
}

func nativeSigningRecoveryMatches(recovery nativecapture.SigningRecovery, secret data.EngineSecretVersion) bool {
	return recovery.Reference == secret.Reference && recovery.Version == secret.Version && recovery.SHA256 == secret.SHA256 && (recovery.Kind == "external_immutable" || recovery.Kind == "encrypted_member" && recovery.Member == "recovery/signing.key")
}
