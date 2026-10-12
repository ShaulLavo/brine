package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

// NativeUploadRetention is private producer input, never a dispatch payload.
// The durable descriptor precedes the one remote write and survives its timeout.
type NativeUploadRetention struct {
	Parent           *os.Root
	Workspace        string
	SourceSealSHA256 string
	SourceEvidence   nativeevidence.Evidence
	ConfirmedBound   spacetimedb.ConfirmedPairsRecord
	FinalPairs       spacetimedb.RecoveredPairs
	Artifact         []byte
	Descriptor       []byte
}

func (NativeUploadRetention) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

type nativeUploadDescriptor struct {
	Format           string                       `json:"format"`
	OperationID      string                       `json:"operation_id"`
	PointID          string                       `json:"point_id"`
	Workspace        string                       `json:"workspace"`
	Directory        data.EngineDirectoryIdentity `json:"directory"`
	SourceSealSHA256 string                       `json:"source_seal_sha256"`
	Source           json.RawMessage              `json:"source"`
	Manifest         json.RawMessage              `json:"manifest"`
	SourceEvidence   json.RawMessage              `json:"source_evidence"`
	ConfirmedBound   json.RawMessage              `json:"confirmed_bound"`
	FinalPairs       json.RawMessage              `json:"final_pairs"`
	ArtifactSHA256   string                       `json:"artifact_sha256"`
	DescriptorSHA256 string                       `json:"descriptor_sha256"`
	Scope            restic.Scope                 `json:"scope"`
	IssuedAt         time.Time                    `json:"issued_at"`
}

func retainNativeUpload(ctx context.Context, tx *sql.Tx, permit EnginePermit, operation, point string, source nativecapture.Source, manifest restic.Manifest, retained NativeUploadRetention) error {
	sourceRaw, err := json.Marshal(source)
	if err != nil || checkNativeBundleMembers(source, manifest, sourceRaw) != nil || !data.ValidCatalogHash(retained.SourceSealSHA256) {
		return ErrConflict
	}
	relative, err := filepath.Rel(string(permit.Reserved.Database.Root), retained.Workspace)
	if err != nil || relative != filepath.Join("native-restores", filepath.Base(relative)) || !data.ValidID(filepath.Base(relative)) {
		return ErrConflict
	}
	identity, err := nativeUploadDirectoryIdentity(string(permit.Reserved.Database.Root), relative, retained.Parent)
	if err != nil {
		return err
	}
	descriptor := nativeUploadDescriptor{Format: "brine-native-upload-descriptor-v1", OperationID: operation, PointID: point, Workspace: relative, Directory: identity, SourceSealSHA256: retained.SourceSealSHA256, Source: sourceRaw, Manifest: manifest.Bytes(), ArtifactSHA256: nativePointHash(retained.Artifact), DescriptorSHA256: nativePointHash(retained.Descriptor), IssuedAt: time.Now().UTC()}
	if source.Kind() == "initialized" {
		schema, schemaErr := spacetimedb.DecodeDescriptorV10(retained.Descriptor, spacetimedb.DefaultSchemaLimits())
		pairs, pairErr := spacetimedb.RecoveryPairsFromPassive(retained.SourceEvidence, schema)
		checkpoint, _, _ := source.Initialized()
		wasm, wire, moduleErr := spacetimedb.ModuleIdentities(retained.Artifact)
		c := checkpoint.Record()
		if schemaErr != nil || pairErr != nil || moduleErr != nil || string(wasm) != c.ArtifactSHA256 || string(wire) != c.RuntimeModuleHash || schema.SHA256() != c.SchemaSHA256 || !bytes.Equal(pairs.Bytes(), retained.FinalPairs.Bytes()) || spacetimedb.CheckConfirmedLowerBound(retained.ConfirmedBound, pairs) != nil || !nativeBundleEvidenceMatches(NativeVerifiedPointView{BindingID: permit.Runtime.BindingID, EpochID: permit.Runtime.EpochID, CommittedOffset: retained.SourceEvidence.Offset()}, checkpoint, retained.SourceEvidence) {
			return ErrConflict
		}
		descriptor.SourceEvidence = retained.SourceEvidence.Bytes()
		descriptor.ConfirmedBound = retained.ConfirmedBound.Bytes()
		descriptor.FinalPairs = pairs.Bytes()
	} else {
		descriptor.SourceEvidence = json.RawMessage("null")
		descriptor.ConfirmedBound = json.RawMessage("null")
		descriptor.FinalPairs = json.RawMessage("null")
	}
	policy, _, err := readNativeBackupPolicy(ctx, tx, permit.Runtime.BindingID)
	if err != nil || policy.EpochID != permit.Runtime.EpochID {
		return ErrConflict
	}
	d := permit.Reserved.Archive.Destination
	location := restic.S3Location{Endpoint: d.Endpoint, Bucket: d.Bucket, Prefix: strings.TrimSuffix(permit.Reserved.Archive.RemotePrefix, "/"), Region: d.Region, PathStyle: d.PathStyle}
	descriptor.Scope = restic.Scope{BindingID: string(permit.Runtime.BindingID), EpochID: string(policy.EpochID), RepositoryID: policy.RepositoryID, DestinationSHA256: location.SHA256(), CredentialVersion: policy.CredentialVersion, RecoveryVersion: policy.RecoveryVersion}
	raw, err := json.Marshal(descriptor)
	if err != nil || len(raw) > 8<<20 || len(retained.Artifact) > 32<<20 || len(retained.Descriptor) > 4<<20 {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_upload_descriptors VALUES(?,?,?,?,?)", operation, point, raw, retained.Artifact, retained.Descriptor)
	return err
}
