package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

type NativeOnlineCapture struct {
	state       *Store
	guard       *data.EngineLock
	operationID string
	pointID     string
	permit      EnginePermit
	capturedAt  time.Time
	inspection  *NativeBackupInspection
}

func (s *Store) BeginNativeOnlineCapture(ctx context.Context, operationID string) (*NativeOnlineCapture, error) {
	input, err := s.ReadNativeTaskInput(ctx, operationID)
	if s.readOnly || err != nil || input.Kind != ops.NativeCapture {
		return nil, errors.Join(ErrConflict, err)
	}
	guard, err := data.AcquireEngineLaunchGuard(ctx, s.dir, input.BindingID)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = guard.Release()
		}
	}()
	permit, err := s.ReadEnginePermit(ctx, input.BindingID)
	if err != nil || permit.Runtime.State != data.EngineInitialized || nativeHasHeldFence(permit.Fences) {
		return nil, ErrConflict
	}
	pointID, err := data.NewID()
	if err != nil {
		return nil, err
	}
	capability := &NativeOnlineCapture{state: s, guard: guard, operationID: operationID, pointID: pointID, permit: permit, capturedAt: time.Now().UTC()}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := refusePendingNativeCaptureMutation(ctx, tx, input.BindingID, operationID); err != nil {
		return nil, err
	}
	if _, err := capability.check(ctx, tx); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(struct {
		PointID     string       `json:"point_id"`
		OperationID string       `json:"operation_id"`
		Runtime     EnginePermit `json:"runtime"`
		At          time.Time    `json:"captured_at"`
	}{pointID, operationID, permit, capability.capturedAt})
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_capture_inputs VALUES(?,?,?,?,'online',?)", pointID, operationID, input.BindingID, permit.Runtime.EpochID, raw)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	complete = true
	return capability, nil
}
func (c *NativeOnlineCapture) Close() error {
	if c == nil || c.guard == nil {
		return nil
	}
	return c.guard.Release()
}
func (c *NativeOnlineCapture) PointID() string {
	if c == nil {
		return ""
	}
	return c.pointID
}
func (c *NativeOnlineCapture) Permit(ctx context.Context) (EnginePermit, error) {
	if c == nil || c.state == nil {
		return EnginePermit{}, ErrConflict
	}
	tx, err := c.state.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EnginePermit{}, err
	}
	defer func() { _ = tx.Rollback() }()
	permit, err := c.check(ctx, tx)
	if err != nil {
		return EnginePermit{}, err
	}
	return permit, tx.Commit()
}
func (c *NativeOnlineCapture) check(ctx context.Context, tx *sql.Tx) (EnginePermit, error) {
	if c == nil || c.state == nil || !c.guard.HoldsLaunch(c.state.dir, c.permit.Runtime.BindingID) {
		return EnginePermit{}, ErrConflict
	}
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", c.operationID))
	if err != nil || op.Kind != ops.NativeCapture || c.inspection == nil && op.State != ops.Preflight || c.inspection != nil && op.State != ops.RecoveryRequired {
		return EnginePermit{}, ErrConflict
	}
	if c.inspection != nil {
		if err := c.inspection.check(ctx, tx); err != nil {
			return EnginePermit{}, err
		}
	}
	input, err := readNativeTaskInput(ctx, tx, op)
	if err != nil || input.BindingID != c.permit.Runtime.BindingID {
		return EnginePermit{}, ErrConflict
	}
	permit, err := readEnginePermit(ctx, tx, input.BindingID)
	before, _ := json.Marshal(c.permit)
	after, _ := json.Marshal(permit)
	if err != nil || !bytes.Equal(before, after) || nativeHasHeldFence(permit.Fences) {
		return EnginePermit{}, ErrConflict
	}
	return permit, nil
}
func (c *NativeOnlineCapture) Material(ctx context.Context) (NativeRepositoryMaterial, error) {
	if c != nil && c.inspection != nil {
		return c.inspection.Material(ctx)
	}
	permit, err := c.Permit(ctx)
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	tx, err := c.state.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	defer func() { _ = tx.Rollback() }()
	material, err := readNativeCurrentRepositoryMaterial(ctx, tx, permit)
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	return material, tx.Commit()
}
func (c *NativeOnlineCapture) AdmitRepository(ctx context.Context, scope restic.Scope, action restic.RepositoryAction, grant string) error {
	if action != restic.ReadRepository && action != restic.WriteSnapshot || grant != "" {
		return ErrConflict
	}
	metadata, err := c.Material(ctx)
	expected := metadata.Scope
	expected.ExpiresAt = scope.ExpiresAt
	if err != nil || expected != scope || scope.ExpiresAt.After(metadata.Scope.ExpiresAt) {
		return ErrConflict
	}
	return nil
}

func (c *NativeOnlineCapture) UploadIntent(ctx context.Context, source nativecapture.Source, manifest restic.Manifest, retained NativeUploadRetention) error {
	if _, err := c.Permit(ctx); err != nil {
		return err
	}
	checkpoint, selection, initialized := source.Initialized()
	if !initialized || selection.Scope().CaptureID != c.pointID || checkpoint.Record().Scope.BindingID != string(c.permit.Runtime.BindingID) {
		return ErrConflict
	}
	tx, cancel, err := c.state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := c.check(ctx, tx); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM data_native_capture_events WHERE capture_id=?", c.pointID).Scan(&count); err != nil || count != 0 {
		return ErrConflict
	}
	if err := retainNativeUpload(ctx, tx, c.permit, c.operationID, c.pointID, source, manifest, retained); err != nil {
		return err
	}
	raw, _ := json.Marshal(struct {
		SourceSHA   string    `json:"source_sha256"`
		ManifestSHA string    `json:"manifest_sha256"`
		At          time.Time `json:"at"`
	}{source.SHA256(), manifest.SHA256(), time.Now().UTC()})
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_capture_events VALUES(?,1,'upload_issued','',?)", c.pointID, raw)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (c *NativeOnlineCapture) Complete(ctx context.Context, extracted restic.AuthenticatedRestore, observed NativePreparedObservation) (NativeVerifiedPointView, error) {
	if c == nil || c.state == nil || c.inspection != nil && c.inspection.resolver == "" {
		return NativeVerifiedPointView{}, ErrConflict
	}
	if _, err := c.Permit(ctx); err != nil {
		return NativeVerifiedPointView{}, err
	}
	metadata, err := c.Material(ctx)
	if err != nil {
		return NativeVerifiedPointView{}, err
	}
	if !extracted.Matches(ctx, observed.Parent, "download", observed.Transport, observed.Manifest) || checkNativePreparedReader(observed, extracted) != nil {
		return NativeVerifiedPointView{}, ErrConflict
	}
	checkpoint, selection, initialized := observed.Source.Initialized()
	record := checkpoint.Record()
	runtime := c.permit.Runtime
	if !initialized || record.Scope.BindingID != string(runtime.BindingID) || record.Scope.EpochID != string(runtime.EpochID) || selection.Scope().CaptureID != c.pointID || record.DatabaseIdentity != string(runtime.Database) || record.PublisherIdentity != string(runtime.Publisher) || record.ArtifactSHA256 != string(runtime.Module.Wasm) || record.RuntimeModuleHash != string(runtime.Module.Program) || record.SchemaSHA256 != runtime.Module.Schema.SHA256 || !nativeSigningRecoveryMatches(record.SigningRecovery, runtime.Secrets.SigningPrivate) {
		return NativeVerifiedPointView{}, ErrConflict
	}
	schema, err := spacetimedb.DecodeDescriptorV10(observed.Descriptor, spacetimedb.DefaultSchemaLimits())
	wasm, wire, moduleErr := spacetimedb.ModuleIdentities(observed.Artifact)
	sourceRaw, _ := json.Marshal(observed.Source)
	transportRaw, _ := json.Marshal(observed.Transport)
	final, finalErr := spacetimedb.RecoveryPairsFromPassive(observed.SourceEvidence, schema)
	restored, restoreErr := spacetimedb.RecoveryPairsFromPassive(observed.RestoredEvidence, schema)
	if err != nil || moduleErr != nil || string(wasm) != record.ArtifactSHA256 || string(wire) != record.RuntimeModuleHash || schema.SHA256() != record.SchemaSHA256 || finalErr != nil || restoreErr != nil || !observed.SourceEvidence.SamePersistedState(observed.RestoredEvidence) || !bytes.Equal(final.Bytes(), observed.FinalPairs.Bytes()) || spacetimedb.CheckConfirmedLowerBound(observed.ConfirmedBound, final) != nil || spacetimedb.CheckRecoveredPairsEqual(final, restored) != nil || checkNativeBundleMembers(observed.Source, observed.Manifest, sourceRaw) != nil {
		return NativeVerifiedPointView{}, ErrConflict
	}
	tx, cancel, err := c.state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return NativeVerifiedPointView{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := c.check(ctx, tx); err != nil {
		return NativeVerifiedPointView{}, err
	}
	if c.inspection != nil {
		if err := checkNativeRetainedUploadObservation(ctx, tx, c.operationID, observed); err != nil {
			return NativeVerifiedPointView{}, err
		}
	}
	var intentRaw []byte
	if err := tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_capture_events WHERE capture_id=? AND stage='upload_issued'", c.pointID).Scan(&intentRaw); err != nil {
		return NativeVerifiedPointView{}, err
	}
	var intent struct {
		SourceSHA   string    `json:"source_sha256"`
		ManifestSHA string    `json:"manifest_sha256"`
		At          time.Time `json:"at"`
	}
	if json.Unmarshal(intentRaw, &intent) != nil || intent.SourceSHA != observed.Source.SHA256() || intent.ManifestSHA != observed.Manifest.SHA256() {
		return NativeVerifiedPointView{}, ErrConflict
	}
	if c.inspection != nil {
		var existingRaw []byte
		existingErr := tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_verified_points WHERE id=? AND operation_id=?", c.pointID, c.operationID).Scan(&existingRaw)
		if existingErr == nil {
			retainedTransport, transportErr := nativeResolvedTransportMatches(ctx, tx, c.pointID, "online", observed.Transport)
			if transportErr != nil {
				return NativeVerifiedPointView{}, transportErr
			}
			existing, decodeErr := decodeNativeVerifiedPoint(existingRaw)
			v := existing.view
			if decodeErr != nil || v.TransportSHA256 != nativePointHash(retainedTransport) || v.SourceSHA256 != observed.Source.SHA256() || v.ManifestSHA256 != observed.Manifest.SHA256() || v.SourceEvidenceSHA256 != observed.SourceEvidence.SHA256() || v.RestoreEvidenceSHA256 != observed.RestoredEvidence.SHA256() {
				return NativeVerifiedPointView{}, ErrConflict
			}
			v.PointSHA256 = existing.hash
			return v, tx.Commit()
		}
		if !errors.Is(existingErr, sql.ErrNoRows) {
			return NativeVerifiedPointView{}, existingErr
		}
	}
	now := time.Now().UTC()
	v := NativeVerifiedPointView{Format: "brine-native-verified-point-v1", PointID: c.pointID, OperationID: c.operationID, BindingID: runtime.BindingID, EpochID: runtime.EpochID, DatabaseID: c.permit.Reserved.Database.DatabaseID, IncarnationID: c.permit.Reserved.Database.IncarnationID, GenerationID: record.GenerationID, RepositoryID: metadata.Scope.RepositoryID, SourceSHA256: observed.Source.SHA256(), CheckpointSHA256: checkpoint.SHA256(), ManifestSHA256: observed.Manifest.SHA256(), TransportSHA256: nativePointHash(transportRaw), SourceEvidenceSHA256: observed.SourceEvidence.SHA256(), RestoreEvidenceSHA256: observed.RestoredEvidence.SHA256(), ConfirmedBoundSHA256: observed.ConfirmedBound.SHA256(), FinalPairsSHA256: final.SHA256(), PolicySHA256: metadata.PolicySHA256, DestinationSHA256: metadata.Scope.DestinationSHA256, CredentialReference: metadata.CredentialReference, CredentialVersion: metadata.Scope.CredentialVersion, RecoveryReference: metadata.RecoveryReference, RecoveryVersion: metadata.Scope.RecoveryVersion, CommittedOffset: observed.SourceEvidence.Offset(), CapturedAt: c.capturedAt, VerifiedAt: now, RetainUntil: now.Add(30 * 24 * time.Hour), LaterLoss: "unknown"}
	if !nativeBundleEvidenceMatches(v, checkpoint, observed.SourceEvidence) || !nativeBundleEvidenceMatches(v, checkpoint, observed.RestoredEvidence) || checkNativeVerifiedBundleRecords(v, sourceRaw, observed.Manifest.Bytes(), transportRaw) != nil || !validNativeVerifiedPoint(v) {
		return NativeVerifiedPointView{}, ErrConflict
	}
	raw, _ := json.Marshal(v)
	id, err := data.NewID()
	if err != nil {
		return NativeVerifiedPointView{}, err
	}
	provenance, _ := json.Marshal(nativeCheckpointProvenance{Format: "brine-native-checkpoint-provenance-v1", PointID: c.pointID, BindingID: runtime.BindingID, EpochID: runtime.EpochID, GenerationID: record.GenerationID, CheckpointSHA256: checkpoint.SHA256(), SourceSHA256: observed.Source.SHA256(), SourceEvidenceSHA256: observed.SourceEvidence.SHA256(), ArtifactSHA256: record.ArtifactSHA256, ProgramWireHash: record.RuntimeModuleHash, DescriptorSHA256: nativePointHash(observed.Descriptor), SchemaSHA256: record.SchemaSHA256, ReaderDisposalSHA256: nativePointHash([]byte(observed.ReaderNonce + observed.ReaderViewSHA256 + observed.ReaderImageID)), ArtifactProofSHA256: nativePointHash(observed.Artifact)})
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_checkpoints VALUES(?,?,?,?,?,?,?,?,?)", id, runtime.BindingID, runtime.EpochID, record.GenerationID, c.operationID, checkpoint.Bytes(), provenance, observed.Artifact, observed.Descriptor)
	if err != nil {
		return NativeVerifiedPointView{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_verified_points VALUES(?,?,?,?,?)", c.pointID, c.operationID, runtime.BindingID, runtime.EpochID, raw)
	if err != nil {
		return NativeVerifiedPointView{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_verified_point_bundles VALUES(?,?,?,?,?,?,?,?)", c.pointID, sourceRaw, observed.Manifest.Bytes(), transportRaw, observed.SourceEvidence.Bytes(), observed.RestoredEvidence.Bytes(), observed.ConfirmedBound.Bytes(), final.Bytes())
	if err != nil {
		return NativeVerifiedPointView{}, err
	}
	for _, material := range []nativeRetainedMaterial{{Format: "brine-native-retained-material-v1", PointID: c.pointID, BindingID: runtime.BindingID, EpochID: runtime.EpochID, Kind: "recovery_password", Reference: metadata.RecoveryReference, Version: metadata.Scope.RecoveryVersion, MaterialSHA256: metadata.RecoverySHA256, DeliveryID: metadata.RecoveryDeliveryID, ManifestSHA256: v.ManifestSHA256, RetainUntil: v.RetainUntil}, {Format: "brine-native-retained-material-v1", PointID: c.pointID, BindingID: runtime.BindingID, EpochID: runtime.EpochID, Kind: "signing_private", Reference: record.SigningRecovery.Reference, Version: record.SigningRecovery.Version, MaterialSHA256: record.SigningRecovery.SHA256, DeliveryID: metadata.RecoveryDeliveryID, ManifestSHA256: v.ManifestSHA256, RetainUntil: v.RetainUntil}} {
		materialRaw, _ := json.Marshal(material)
		if _, err := tx.ExecContext(ctx, "INSERT INTO data_native_recovery_retentions VALUES(?,?,?,?)", c.pointID, material.Reference, material.Version, materialRaw); err != nil {
			return NativeVerifiedPointView{}, err
		}
	}
	if !extracted.Matches(ctx, observed.Parent, "download", observed.Transport, observed.Manifest) {
		return NativeVerifiedPointView{}, ErrConflict
	}
	v.PointSHA256 = nativePointHash(raw)
	return v, tx.Commit()
}

func (s *Store) ReadNativeOnlinePreparation(ctx context.Context, capability *NativeOnlineCapture) (*EnginePreparation, error) {
	if capability == nil || capability.state != s {
		return nil, ErrConflict
	}
	if _, err := capability.Permit(ctx); err != nil {
		return nil, err
	}
	return &EnginePreparation{root: s.dir, binding: capability.permit.Runtime.BindingID, guard: capability.guard}, nil
}
