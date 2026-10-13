package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/ShaulLavo/brine/internal/spacetimedb"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/restic"
)

// NativePreparedBundle is protected retained input, selected only through an
// actual completed stopped capability. It supplies no independent effect grant.
type NativePreparedBundle struct {
	point            nativePreparedPoint
	source           nativecapture.Source
	manifest         restic.Manifest
	transport        restic.TransportPoint
	sourceEvidence   nativeevidence.Evidence
	restoredEvidence nativeevidence.Evidence
	confirmedBound   spacetimedb.ConfirmedPairsRecord
	finalPairs       spacetimedb.RecoveredPairs
	schema           spacetimedb.CanonicalSchema
}

func (b NativePreparedBundle) Point() NativePreparedPointView {
	v := b.point.view
	v.PointSHA256 = b.point.hash
	v.Names = slices.Clone(v.Names)
	return v
}
func (b NativePreparedBundle) Source() nativecapture.Source              { return b.source }
func (b NativePreparedBundle) Manifest() restic.Manifest                 { return b.manifest }
func (b NativePreparedBundle) Transport() restic.TransportPoint          { return b.transport }
func (b NativePreparedBundle) SourceEvidence() nativeevidence.Evidence   { return b.sourceEvidence }
func (b NativePreparedBundle) RestoredEvidence() nativeevidence.Evidence { return b.restoredEvidence }
func (b NativePreparedBundle) ConfirmedBound() spacetimedb.ConfirmedPairsRecord {
	return b.confirmedBound
}
func (b NativePreparedBundle) FinalPairs() spacetimedb.RecoveredPairs { return b.finalPairs }
func (b NativePreparedBundle) Schema() spacetimedb.CanonicalSchema    { return b.schema }

func (s *Store) ReadNativePreparedBundle(ctx context.Context, stopped *NativeStoppedPreparation) (NativePreparedBundle, error) {
	if !stopped.active(s) || stopped.completedPointSHA256 == "" {
		return NativePreparedBundle{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativePreparedBundle{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkNativeStoppedCapability(ctx, tx, stopped); err != nil {
		return NativePreparedBundle{}, err
	}
	bundle, err := readNativePreparedBundleTx(ctx, tx, stopped.PointID())
	if err != nil || !stopped.active(s) || bundle.point.hash != stopped.completedPointSHA256 || checkNativePreparedSourceRuntime(ctx, tx, stopped, bundle.source) != nil {
		return NativePreparedBundle{}, errors.Join(ErrConflict, err)
	}
	return bundle, tx.Commit()
}

func (s *Store) ReadNativeArchivedBundle(ctx context.Context, archived *NativeArchivedSource) (NativePreparedBundle, error) {
	if archived == nil || archived.state != s {
		return NativePreparedBundle{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativePreparedBundle{}, err
	}
	defer func() { _ = tx.Rollback() }()
	pointID, err := checkNativeArchivedSourceTx(ctx, tx, archived)
	if err != nil {
		return NativePreparedBundle{}, err
	}
	bundle, err := readNativePreparedBundleTx(ctx, tx, pointID)
	if err != nil {
		return NativePreparedBundle{}, err
	}
	if _, err := checkNativeArchivedSourceTx(ctx, tx, archived); err != nil {
		return NativePreparedBundle{}, err
	}
	return bundle, tx.Commit()
}

func readNativePreparedBundleTx(ctx context.Context, tx dataQuerier, pointID string) (NativePreparedBundle, error) {
	point, err := readNativePreparedPointTx(ctx, tx, pointID)
	if err != nil {
		return NativePreparedBundle{}, err
	}
	var sourceRaw, manifestRaw, transportRaw, sourceEvidenceRaw, restoredEvidenceRaw, confirmedRaw, finalRaw []byte
	err = tx.QueryRowContext(ctx, "SELECT source,manifest,transport,source_evidence,restored_evidence,confirmed_bound,final_pairs FROM data_native_prepared_point_bundles WHERE point_id=?", pointID).Scan(&sourceRaw, &manifestRaw, &transportRaw, &sourceEvidenceRaw, &restoredEvidenceRaw, &confirmedRaw, &finalRaw)
	if err != nil {
		return NativePreparedBundle{}, errors.Join(&IntegrityError{}, err)
	}
	bundle, err := decodeNativePreparedBundle(ctx, tx, point, sourceRaw, manifestRaw, transportRaw, sourceEvidenceRaw, restoredEvidenceRaw, confirmedRaw, finalRaw)
	if err != nil {
		return NativePreparedBundle{}, err
	}
	if err := checkNativePreparedBundleVerification(ctx, tx, point, bundle, sourceEvidenceRaw, restoredEvidenceRaw, confirmedRaw, finalRaw); err != nil {
		return NativePreparedBundle{}, err
	}
	return bundle, nil
}

func decodeNativePreparedBundle(ctx context.Context, tx dataQuerier, point nativePreparedPoint, sourceRaw, manifestRaw, transportRaw, sourceEvidenceRaw, restoredEvidenceRaw, confirmedRaw, finalRaw []byte) (NativePreparedBundle, error) {
	pointID := point.view.PointID
	source, err := nativecapture.DecodeSource(sourceRaw, nativecapture.DefaultLimits())
	if err != nil || source.SHA256() != point.view.SourceSHA256 || source.Kind() != point.view.SourceKind {
		return NativePreparedBundle{}, &IntegrityError{}
	}
	manifest, err := restic.DecodeManifest(manifestRaw, restic.DefaultLimits())
	var transport restic.TransportPoint
	if err != nil || !bytes.Equal(manifest.Bytes(), manifestRaw) || manifest.SHA256() != point.view.BundleManifestSHA256 || json.Unmarshal(transportRaw, &transport) != nil {
		return NativePreparedBundle{}, &IntegrityError{}
	}
	canonicalTransport, err := json.Marshal(transport)
	if err != nil || !bytes.Equal(canonicalTransport, transportRaw) || nativePointHash(transportRaw) != point.view.TransportPointSHA256 || transport.Identity != manifest.Identity() || transport.Identity.CaptureID != pointID || transport.Identity.BindingID != string(point.view.BindingID) || transport.Identity.EpochID != string(point.view.EpochID) || transport.Identity.CheckpointSHA256 != point.view.CheckpointSHA256 || transport.ManifestSHA256 != point.view.BundleManifestSHA256 {
		return NativePreparedBundle{}, &IntegrityError{}
	}
	bundle := NativePreparedBundle{point: point, source: source, manifest: manifest, transport: transport}
	if checkNativeBundleMembers(source, manifest, sourceRaw) != nil {
		return NativePreparedBundle{}, &IntegrityError{}
	}
	if point.view.SourceKind == "unpublished" {
		empty, valid := source.Empty()
		if !valid || empty.GenerationID != point.view.GenerationID || empty.ConfigurationSHA256 != point.view.ConfigSHA256 || nativeSigningRecoverySHA256(empty.SigningRecovery) != point.view.SigningRecoverySHA256 || empty.Scope.BindingID != string(point.view.BindingID) || empty.Scope.EpochID != string(point.view.EpochID) || transport.Identity.ModuleSHA256 != source.SHA256() || nativePointHash(sourceRaw) != point.view.CheckpointSHA256 || len(sourceEvidenceRaw) != 0 || len(restoredEvidenceRaw) != 0 || len(confirmedRaw) != 0 || len(finalRaw) != 0 {
			return NativePreparedBundle{}, &IntegrityError{}
		}
		return bundle, nil
	}
	checkpoint, selection, valid := source.Initialized()
	c := checkpoint.Record()
	if !valid || checkpoint.SHA256() != point.view.CheckpointSHA256 || c.Scope.BindingID != string(point.view.BindingID) || c.Scope.EpochID != string(point.view.EpochID) || transport.Identity.ModuleSHA256 != c.ArtifactSHA256 || c.GenerationID != point.view.GenerationID || c.DatabaseIdentity != point.view.NativeDatabase || c.PublisherIdentity != point.view.NativePublisher || c.ArtifactSHA256 != point.view.ArtifactWasmSHA256 || c.RuntimeModuleHash != point.view.ProgramWireHash || c.SchemaSHA256 != point.view.SchemaSHA256 || c.ConfigurationSHA256 != point.view.ConfigSHA256 || nativeSigningRecoverySHA256(c.SigningRecovery) != point.view.SigningRecoverySHA256 || !slices.Equal(c.Names, point.view.Names) || selection.Scope().CaptureID != pointID || checkNativeBundleMembers(source, manifest, sourceRaw) != nil {
		return NativePreparedBundle{}, &IntegrityError{}
	}
	sourceEvidence, sourceErr := nativeevidence.DecodeStored(sourceEvidenceRaw)
	restoredEvidence, restoredErr := nativeevidence.DecodeStored(restoredEvidenceRaw)
	if sourceErr != nil || restoredErr != nil || !bytes.Equal(sourceEvidence.Bytes(), sourceEvidenceRaw) || !bytes.Equal(restoredEvidence.Bytes(), restoredEvidenceRaw) || !sourceEvidence.SamePersistedState(restoredEvidence) || sourceEvidence.SHA256() != point.view.SourceWitnessSHA256 || sourceEvidence.Offset() != point.view.CommittedOffset || sourceEvidence.DatabaseIdentity() != c.DatabaseIdentity || sourceEvidence.OwnerIdentity() != c.PublisherIdentity || sourceEvidence.Program().Kind != "wasm" || sourceEvidence.Program().SHA256 != c.ArtifactSHA256 || sourceEvidence.Program().Wire != c.RuntimeModuleHash {
		return NativePreparedBundle{}, &IntegrityError{}
	}
	schema, err := readNativePreparedSchema(ctx, tx, checkpoint)
	confirmed, confirmedErr := spacetimedb.DecodeConfirmedPairsRecord(confirmedRaw)
	final, finalErr := spacetimedb.RecoveryPairsFromPassive(sourceEvidence, schema)
	restored, restoredErr := spacetimedb.RecoveryPairsFromPassive(restoredEvidence, schema)
	if err != nil || confirmedErr != nil || finalErr != nil || restoredErr != nil || !slices.Equal(confirmed.Names(), c.Names) || !bytes.Equal(final.Bytes(), finalRaw) || spacetimedb.CheckConfirmedLowerBound(confirmed, final) != nil || spacetimedb.CheckRecoveredPairsEqual(final, restored) != nil {
		return NativePreparedBundle{}, &IntegrityError{}
	}
	bundle.sourceEvidence, bundle.restoredEvidence = sourceEvidence, restoredEvidence
	bundle.schema, bundle.confirmedBound, bundle.finalPairs = schema, confirmed, final
	return bundle, nil
}

func readNativePreparedSchema(ctx context.Context, q dataQuerier, checkpoint nativecapture.Checkpoint) (spacetimedb.CanonicalSchema, error) {
	c := checkpoint.Record()
	var canonical, artifact, descriptor []byte
	err := q.QueryRowContext(ctx, "SELECT canonical,artifact,descriptor FROM data_native_checkpoints WHERE binding_id=? AND epoch_id=? AND generation_id=?", c.Scope.BindingID, c.Scope.EpochID, c.GenerationID).Scan(&canonical, &artifact, &descriptor)
	wasm, wire, moduleErr := spacetimedb.ModuleIdentities(artifact)
	schema, schemaErr := spacetimedb.DecodeDescriptorV10(descriptor, spacetimedb.DefaultSchemaLimits())
	if err != nil || moduleErr != nil || schemaErr != nil || !bytes.Equal(canonical, checkpoint.Bytes()) || string(wasm) != c.ArtifactSHA256 || string(wire) != c.RuntimeModuleHash || schema.Version() != c.SchemaVersion || schema.SHA256() != c.SchemaSHA256 {
		return spacetimedb.CanonicalSchema{}, errors.Join(&IntegrityError{}, err)
	}
	return schema, nil
}

func nativeSigningRecoverySHA256(recovery nativecapture.SigningRecovery) string {
	raw, _ := json.Marshal(recovery)
	return nativePointHash(raw)
}
