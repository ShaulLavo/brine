package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type nativeVerifiedPointBundle struct {
	point            nativeVerifiedPoint
	source           nativecapture.Source
	checkpoint       nativecapture.Checkpoint
	selection        nativecapture.Selection
	manifest         restic.Manifest
	transport        restic.TransportPoint
	sourceEvidence   nativeevidence.Evidence
	restoredEvidence nativeevidence.Evidence
	confirmedBound   spacetimedb.ConfirmedPairsRecord
	finalPairs       spacetimedb.RecoveredPairs
	dependencies     nativeRetainedDependencies
}

type nativeRetainedDependencies struct {
	artifactSHA256   string
	descriptorSHA256 string
	checkpointSHA256 string
	recoveryRef      string
	recoveryVersion  uint64
	signingRef       string
	signingVersion   uint64
	signingSHA256    string
	retainUntil      time.Time
	hash             string
}

func readNativeVerifiedPointBundleTx(ctx context.Context, tx *sql.Tx, pointID string) (nativeVerifiedPointBundle, error) {
	point, err := readNativeVerifiedPointTx(ctx, tx, pointID)
	if err != nil {
		return nativeVerifiedPointBundle{}, err
	}
	var sourceRaw, manifestRaw, transportRaw, sourceEvidenceRaw, restoredEvidenceRaw, confirmedRaw, finalRaw []byte
	err = tx.QueryRowContext(ctx, "SELECT source,manifest,transport,source_evidence,restored_evidence,confirmed_bound,final_pairs FROM data_native_verified_point_bundles WHERE point_id=?", pointID).Scan(&sourceRaw, &manifestRaw, &transportRaw, &sourceEvidenceRaw, &restoredEvidenceRaw, &confirmedRaw, &finalRaw)
	if err != nil {
		return nativeVerifiedPointBundle{}, err
	}
	source, err := nativecapture.DecodeSource(sourceRaw, nativecapture.DefaultLimits())
	checkpoint, selection, initialized := source.Initialized()
	if err != nil || !initialized {
		return nativeVerifiedPointBundle{}, &IntegrityError{}
	}
	manifest, err := restic.DecodeManifest(manifestRaw, restic.DefaultLimits())
	var transport restic.TransportPoint
	if err != nil || json.Unmarshal(transportRaw, &transport) != nil {
		return nativeVerifiedPointBundle{}, &IntegrityError{}
	}
	if err := checkNativeBundleMembers(source, manifest, sourceRaw); err != nil {
		return nativeVerifiedPointBundle{}, err
	}
	schema, dependencies, err := readNativeVerifiedDependencies(ctx, tx, point, checkpoint)
	if err != nil {
		return nativeVerifiedPointBundle{}, err
	}
	sourceEvidence, sourceErr := nativeevidence.Decode(sourceEvidenceRaw, nativeevidence.MaximumLimits())
	restoredEvidence, restoredErr := nativeevidence.Decode(restoredEvidenceRaw, nativeevidence.MaximumLimits())
	if sourceErr != nil || restoredErr != nil || !bytes.Equal(sourceEvidence.Bytes(), sourceEvidenceRaw) || !bytes.Equal(restoredEvidence.Bytes(), restoredEvidenceRaw) || !sourceEvidence.SamePersistedState(restoredEvidence) || !nativeBundleEvidenceMatches(point.view, checkpoint, sourceEvidence) || !nativeBundleEvidenceMatches(point.view, checkpoint, restoredEvidence) {
		return nativeVerifiedPointBundle{}, &IntegrityError{}
	}
	confirmed, err := spacetimedb.DecodeConfirmedPairsRecord(confirmedRaw)
	if err != nil || !slices.Equal(confirmed.Names(), checkpoint.Record().Names) {
		return nativeVerifiedPointBundle{}, &IntegrityError{}
	}
	final, sourceErr := spacetimedb.RecoveryPairsFromPassive(sourceEvidence, schema)
	restored, restoredErr := spacetimedb.RecoveryPairsFromPassive(restoredEvidence, schema)
	if sourceErr != nil || restoredErr != nil || !bytes.Equal(final.Bytes(), finalRaw) || spacetimedb.CheckConfirmedLowerBound(confirmed, final) != nil || spacetimedb.CheckRecoveredPairsEqual(final, restored) != nil {
		return nativeVerifiedPointBundle{}, &IntegrityError{}
	}
	return nativeVerifiedPointBundle{point: point, source: source, checkpoint: checkpoint, selection: selection, manifest: manifest, transport: transport, sourceEvidence: sourceEvidence, restoredEvidence: restoredEvidence, confirmedBound: confirmed, finalPairs: final, dependencies: dependencies}, nil
}

func nativeBundleEvidenceMatches(v NativeVerifiedPointView, checkpoint nativecapture.Checkpoint, evidence nativeevidence.Evidence) bool {
	c := checkpoint.Record()
	program := evidence.Program()
	return evidence.DatabaseIdentity() == c.DatabaseIdentity && evidence.OwnerIdentity() == c.PublisherIdentity && program.Kind == "wasm" && program.Wire == c.RuntimeModuleHash && program.SHA256 == c.ArtifactSHA256 && evidence.Offset() == v.CommittedOffset
}

func readNativeVerifiedDependencies(ctx context.Context, q dataQuerier, point nativeVerifiedPoint, checkpoint nativecapture.Checkpoint) (spacetimedb.CanonicalSchema, nativeRetainedDependencies, error) {
	c := checkpoint.Record()
	var checkpointRaw, provenance, artifact, descriptor []byte
	err := q.QueryRowContext(ctx, "SELECT canonical,provenance,artifact,descriptor FROM data_native_checkpoints WHERE binding_id=? AND epoch_id=? AND generation_id=?", point.view.BindingID, point.view.EpochID, point.view.GenerationID).Scan(&checkpointRaw, &provenance, &artifact, &descriptor)
	if err != nil || !bytes.Equal(checkpointRaw, checkpoint.Bytes()) || len(provenance) == 0 || len(provenance) > 65536 || !json.Valid(provenance) {
		return spacetimedb.CanonicalSchema{}, nativeRetainedDependencies{}, errors.Join(&IntegrityError{}, err)
	}
	wasm, wire, err := spacetimedb.ModuleIdentities(artifact)
	if err != nil || string(wasm) != c.ArtifactSHA256 || string(wire) != c.RuntimeModuleHash {
		return spacetimedb.CanonicalSchema{}, nativeRetainedDependencies{}, &IntegrityError{}
	}
	schema, err := spacetimedb.DecodeDescriptorV10(descriptor, spacetimedb.DefaultSchemaLimits())
	if err != nil || schema.Version() != c.SchemaVersion || schema.SHA256() != c.SchemaSHA256 {
		return spacetimedb.CanonicalSchema{}, nativeRetainedDependencies{}, &IntegrityError{}
	}
	if err := checkNativeCheckpointProvenance(provenance, point.view, checkpoint, descriptor); err != nil {
		return spacetimedb.CanonicalSchema{}, nativeRetainedDependencies{}, err
	}
	dependencies := nativeRetainedDependencies{artifactSHA256: c.ArtifactSHA256, descriptorSHA256: nativePointHash(descriptor), checkpointSHA256: checkpoint.SHA256(), recoveryRef: point.view.RecoveryReference, recoveryVersion: point.view.RecoveryVersion, signingRef: c.SigningRecovery.Reference, signingVersion: c.SigningRecovery.Version, signingSHA256: c.SigningRecovery.SHA256, retainUntil: point.view.RetainUntil}
	var recoveryRetention, signingRetention []byte
	err = q.QueryRowContext(ctx, "SELECT canonical FROM data_native_recovery_retentions WHERE point_id=? AND reference=? AND version=?", point.view.PointID, dependencies.recoveryRef, dependencies.recoveryVersion).Scan(&recoveryRetention)
	if err == nil {
		err = q.QueryRowContext(ctx, "SELECT canonical FROM data_native_recovery_retentions WHERE point_id=? AND reference=? AND version=?", point.view.PointID, dependencies.signingRef, dependencies.signingVersion).Scan(&signingRetention)
	}
	if err != nil || len(recoveryRetention) == 0 || len(signingRetention) == 0 || len(recoveryRetention) > 16384 || len(signingRetention) > 16384 {
		return spacetimedb.CanonicalSchema{}, nativeRetainedDependencies{}, errors.Join(&IntegrityError{}, err)
	}
	recovery, err := decodeNativeRetainedMaterial(recoveryRetention, point.view, "recovery_password", dependencies.recoveryRef, dependencies.recoveryVersion)
	if err != nil {
		return spacetimedb.CanonicalSchema{}, nativeRetainedDependencies{}, err
	}
	signing, err := decodeNativeRetainedMaterial(signingRetention, point.view, "signing_private", dependencies.signingRef, dependencies.signingVersion)
	if err != nil || signing.MaterialSHA256 != dependencies.signingSHA256 || signing.DeliveryID != recovery.DeliveryID || c.SigningRecovery.Kind != "encrypted_member" || c.SigningRecovery.Member != "recovery/signing.key" {
		return spacetimedb.CanonicalSchema{}, nativeRetainedDependencies{}, errors.Join(&IntegrityError{}, err)
	}
	if err := checkNativeRetainedRecoveryDelivery(ctx, q, recovery, point.view.CapturedAt); err != nil {
		return spacetimedb.CanonicalSchema{}, nativeRetainedDependencies{}, err
	}
	dependencies.hash = nativePointHash(bytes.Join([][]byte{provenance, recoveryRetention, signingRetention}, []byte{0}))
	return schema, dependencies, nil
}

type nativeCheckpointProvenance struct {
	Format               string                `json:"format"`
	PointID              string                `json:"point_id"`
	BindingID            data.ReplicaBindingID `json:"binding_id"`
	EpochID              data.ReplicaEpochID   `json:"epoch_id"`
	GenerationID         string                `json:"generation_id"`
	CheckpointSHA256     string                `json:"checkpoint_sha256"`
	SourceSHA256         string                `json:"source_sha256"`
	SourceEvidenceSHA256 string                `json:"source_evidence_sha256"`
	ArtifactSHA256       string                `json:"artifact_sha256"`
	ProgramWireHash      string                `json:"program_wire_hash"`
	DescriptorSHA256     string                `json:"descriptor_sha256"`
	SchemaSHA256         string                `json:"schema_sha256"`
	ReaderDisposalSHA256 string                `json:"reader_disposal_sha256"`
	ArtifactProofSHA256  string                `json:"artifact_proof_sha256"`
}

func checkNativeCheckpointProvenance(raw []byte, point NativeVerifiedPointView, checkpoint nativecapture.Checkpoint, descriptor []byte) error {
	fields := []string{"format", "point_id", "binding_id", "epoch_id", "generation_id", "checkpoint_sha256", "source_sha256", "source_evidence_sha256", "artifact_sha256", "program_wire_hash", "descriptor_sha256", "schema_sha256", "reader_disposal_sha256", "artifact_proof_sha256"}
	var proof nativeCheckpointProvenance
	if _, err := strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, &proof) != nil {
		return &IntegrityError{}
	}
	canonical, err := json.Marshal(proof)
	c := checkpoint.Record()
	if err != nil || !bytes.Equal(raw, canonical) || proof.Format != "brine-native-checkpoint-provenance-v1" || proof.PointID != point.PointID || proof.BindingID != point.BindingID || proof.EpochID != point.EpochID || proof.GenerationID != point.GenerationID || proof.CheckpointSHA256 != checkpoint.SHA256() || proof.SourceSHA256 != point.SourceSHA256 || proof.SourceEvidenceSHA256 != point.SourceEvidenceSHA256 || proof.ArtifactSHA256 != c.ArtifactSHA256 || proof.ProgramWireHash != c.RuntimeModuleHash || proof.DescriptorSHA256 != nativePointHash(descriptor) || proof.SchemaSHA256 != c.SchemaSHA256 || !data.ValidCatalogHash(proof.ReaderDisposalSHA256) || !data.ValidCatalogHash(proof.ArtifactProofSHA256) {
		return &IntegrityError{}
	}
	return nil
}

type nativeRetainedMaterial struct {
	Format         string                `json:"format"`
	PointID        string                `json:"point_id"`
	BindingID      data.ReplicaBindingID `json:"binding_id"`
	EpochID        data.ReplicaEpochID   `json:"epoch_id"`
	Kind           string                `json:"kind"`
	Reference      string                `json:"reference"`
	Version        uint64                `json:"version"`
	MaterialSHA256 string                `json:"material_sha256"`
	DeliveryID     string                `json:"delivery_id"`
	ManifestSHA256 string                `json:"manifest_sha256"`
	RetainUntil    time.Time             `json:"retain_until"`
}

func decodeNativeRetainedMaterial(raw []byte, point NativeVerifiedPointView, kind, reference string, version uint64) (nativeRetainedMaterial, error) {
	var value nativeRetainedMaterial
	fields := []string{"format", "point_id", "binding_id", "epoch_id", "kind", "reference", "version", "material_sha256", "delivery_id", "manifest_sha256", "retain_until"}
	if _, err := strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, &value) != nil {
		return value, &IntegrityError{}
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(raw, canonical) || value.Format != "brine-native-retained-material-v1" || value.PointID != point.PointID || value.BindingID != point.BindingID || value.EpochID != point.EpochID || value.Kind != kind || value.Reference != reference || value.Version != version || !data.ValidCatalogHash(value.MaterialSHA256) || !data.ValidID(value.DeliveryID) || value.ManifestSHA256 != point.ManifestSHA256 || value.RetainUntil.Location() != time.UTC || value.RetainUntil.Before(point.RetainUntil) {
		return value, &IntegrityError{}
	}
	return value, nil
}

type nativeRecoveryDeliveryRecord struct {
	Format      string                `json:"format"`
	ID          string                `json:"id"`
	OperationID string                `json:"operation_id"`
	Requester   string                `json:"requester"`
	BindingID   data.ReplicaBindingID `json:"binding_id"`
	EpochID     data.ReplicaEpochID   `json:"epoch_id"`
	Reference   string                `json:"reference"`
	Version     uint64                `json:"version"`
	SHA256      string                `json:"sha256"`
	ReceivedAt  time.Time             `json:"received_at"`
}

func checkNativeRetainedRecoveryDelivery(ctx context.Context, q dataQuerier, retention nativeRetainedMaterial, capturedAt time.Time) error {
	var operation, requester, reference string
	var binding data.ReplicaBindingID
	var epoch data.ReplicaEpochID
	var version uint64
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT operation_id,requester,binding_id,epoch_id,reference,version,canonical FROM data_native_recovery_deliveries WHERE id=?", retention.DeliveryID).Scan(&operation, &requester, &binding, &epoch, &reference, &version, &raw)
	if err != nil || binding != retention.BindingID || reference != retention.Reference || version != retention.Version || len(raw) == 0 || len(raw) > 16384 {
		return errors.Join(&IntegrityError{}, err)
	}
	var delivery nativeRecoveryDeliveryRecord
	fields := []string{"format", "id", "operation_id", "requester", "binding_id", "epoch_id", "reference", "version", "sha256", "received_at"}
	if _, err := strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, &delivery) != nil {
		return &IntegrityError{}
	}
	canonical, err := json.Marshal(delivery)
	if err != nil || !bytes.Equal(raw, canonical) || delivery.Format != "brine-native-recovery-delivery-v1" || delivery.ID != retention.DeliveryID || delivery.OperationID != operation || delivery.Requester != requester || delivery.BindingID != binding || delivery.EpochID != epoch || delivery.Reference != reference || delivery.Version != version || delivery.SHA256 != retention.MaterialSHA256 || delivery.ReceivedAt.IsZero() || delivery.ReceivedAt.Location() != time.UTC || delivery.ReceivedAt.After(capturedAt) {
		return &IntegrityError{}
	}
	if err := checkNativeRecoveryMaterialOperation(ctx, q, delivery); err != nil {
		return err
	}
	if epoch != retention.EpochID {
		return checkNativeRetainedRepositoryDelivery(ctx, q, retention)
	}
	return nil
}

const nativeSourceMember = "native-source.json"

func checkNativeBundleMembers(source nativecapture.Source, manifest restic.Manifest, sourceRaw []byte) error {
	checkpoint, selection, initialized := source.Initialized()
	var members []nativecapture.CheckpointMember
	if initialized {
		members = checkpoint.Record().Members
	} else if empty, valid := source.Empty(); valid {
		members = empty.Members
	} else {
		return &IntegrityError{}
	}
	expected := map[string]restic.Member{nativeSourceMember: {Path: nativeSourceMember, Type: "file", Mode: 0600, Size: int64(len(sourceRaw)), SHA256: source.SHA256()}}
	for _, member := range members {
		if member.Bytes > math.MaxInt64 {
			return &IntegrityError{}
		}
		expected[member.Path] = restic.Member{Path: member.Path, Type: "file", Mode: 0600, Size: int64(member.Bytes), SHA256: member.SHA256}
	}
	for _, member := range selection.Members() {
		if member.Bytes > math.MaxInt64 {
			return &IntegrityError{}
		}
		if _, duplicate := expected[member.Path]; duplicate {
			return &IntegrityError{}
		}
		expected[member.Path] = restic.Member{Path: member.Path, Type: "file", Mode: 0600, Size: int64(member.Bytes), SHA256: member.SHA256}
	}
	for name := range expected {
		if err := addNativeBundleParentDirectories(expected, name); err != nil {
			return err
		}
	}
	if len(expected) != len(manifest.Members()) {
		return &IntegrityError{}
	}
	for _, member := range manifest.Members() {
		if expected[member.Path] != member {
			return &IntegrityError{}
		}
	}
	return nil
}

func addNativeBundleParentDirectories(expected map[string]restic.Member, name string) error {
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if member, exists := expected[parent]; exists && member.Type != "directory" {
			return &IntegrityError{}
		}
		expected[parent] = restic.Member{Path: parent, Type: "directory", Mode: 0700}
	}
	return nil
}
