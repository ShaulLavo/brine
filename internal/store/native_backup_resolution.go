package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

type NativeBackupInspection struct {
	state          *Store
	guard          *data.EngineLock
	original       ops.Operation
	binding        data.ReplicaBindingID
	originalSHA256 string
	resolver       string
	review         nativecapture.BackupResolutionReview
	deadline       time.Time
	activation     *NativeBackupActivation
}

func (i *NativeBackupInspection) Close() error {
	if i == nil || i.guard == nil {
		return nil
	}
	return i.guard.Release()
}
func (i *NativeBackupInspection) OriginalKind() ops.Kind           { return i.original.Kind }
func (i *NativeBackupInspection) BindingID() data.ReplicaBindingID { return i.binding }
func (i *NativeBackupInspection) OriginalOperationID() string      { return i.original.ID }

type NativeRetainedUpload struct {
	Source           nativecapture.Source
	Manifest         restic.Manifest
	Workspace        string
	Scope            restic.Scope
	Directory        data.EngineDirectoryIdentity
	SourceSealSHA256 string
	SourceEvidence   nativeevidence.Evidence
	ConfirmedBound   spacetimedb.ConfirmedPairsRecord
	FinalPairs       spacetimedb.RecoveredPairs
	Artifact         []byte
	Descriptor       []byte
}

func (NativeRetainedUpload) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

func nativeResolutionOperation(ctx context.Context, q dataQuerier, id string) (ops.Operation, error) {
	return scanOperation(q.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", id))
}

func nativeBackupOriginal(ctx context.Context, q dataQuerier, id, requester string) (ops.Operation, data.ReplicaBindingID, string, error) {
	op, err := nativeResolutionOperation(ctx, q, id)
	if err != nil || op.Requester != requester || op.State != ops.RecoveryRequired || op.Kind != ops.NativeCapture && op.Kind != ops.NativePrepare && op.Kind != ops.NativeBackupActivate {
		return op, "", "", ErrConflict
	}
	var identity, outcome []byte
	var binding data.ReplicaBindingID
	if op.Kind == ops.NativeBackupActivate {
		err = q.QueryRowContext(ctx, "SELECT p.binding_id,c.canonical FROM data_native_backup_revision_claims c JOIN data_native_backup_revision_plans p ON p.id=c.plan_id WHERE c.operation_id=? AND c.plan_id=?", id, op.SecretRef).Scan(&binding, &identity)
	} else {
		input, inputErr := readNativeTaskInput(ctx, q, op)
		if inputErr != nil {
			return op, "", "", inputErr
		}
		binding = input.BindingID
		err = q.QueryRowContext(ctx, "SELECT canonical FROM data_native_upload_descriptors WHERE operation_id=?", id).Scan(&identity)
	}
	if err != nil {
		return op, "", "", ErrConflict
	}
	if err = q.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", id).Scan(&outcome); err != nil {
		return op, "", "", ErrConflict
	}
	if _, err = ops.DecodeTaskOutcome(op, outcome); err != nil {
		return op, "", "", &IntegrityError{}
	}
	original, _ := json.Marshal(op)
	return op, binding, nativePointHash(bytes.Join([][]byte{original, identity, outcome}, nil)), nil
}

func (s *Store) OpenNativeBackupInspection(ctx context.Context, original, requester string) (*NativeBackupInspection, error) {
	op, binding, hash, err := nativeBackupOriginal(ctx, s.db, original, requester)
	if err != nil {
		return nil, err
	}
	guard, err := data.AcquireEngineLaunchGuard(ctx, s.dir, binding)
	if err != nil {
		return nil, err
	}
	i := &NativeBackupInspection{state: s, guard: guard, original: op, binding: binding, originalSHA256: hash, deadline: time.Now().UTC().Add(10 * time.Minute)}
	if err = i.check(ctx, s.db); err != nil {
		_ = guard.Release()
		return nil, err
	}
	return i, nil
}

func (i *NativeBackupInspection) check(ctx context.Context, q dataQuerier) error {
	if i == nil || i.state == nil || !i.guard.HoldsLaunch(i.state.dir, i.binding) {
		return ErrConflict
	}
	op, binding, hash, err := nativeBackupOriginal(ctx, q, i.original.ID, i.original.Requester)
	if err != nil || binding != i.binding || hash != i.originalSHA256 || op.SecretRef != i.original.SecretRef {
		return ErrConflict
	}
	if i.resolver == "" {
		return nil
	}
	resolve, err := nativeResolutionOperation(ctx, q, i.resolver)
	if err != nil || resolve.Kind != ops.NativeBackupResolve || resolve.State != ops.Preflight || resolve.RecoveryOf != op.ID || resolve.Requester != op.Requester || resolve.App != op.App || resolve.SecretRef != i.review.ID || !time.Now().UTC().Before(i.review.ExpiresAt) || i.review.OriginalSHA256 != hash {
		return ErrConflict
	}
	var raw []byte
	if err = q.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_resolution_events WHERE operation_id=? AND stage='claimed'", resolve.ID).Scan(&raw); err != nil {
		return ErrConflict
	}
	expected, _ := json.Marshal(i.review)
	if !bytes.Equal(raw, expected) {
		return &IntegrityError{}
	}
	return nil
}

func (i *NativeBackupInspection) Upload(ctx context.Context) (NativeRetainedUpload, error) {
	var v NativeRetainedUpload
	if err := i.check(ctx, i.state.db); err != nil {
		return v, err
	}
	var raw, artifact, descriptor []byte
	if err := i.state.db.QueryRowContext(ctx, "SELECT canonical,artifact,descriptor FROM data_native_upload_descriptors WHERE operation_id=?", i.original.ID).Scan(&raw, &artifact, &descriptor); err != nil {
		return v, ErrConflict
	}
	var d nativeUploadDescriptor
	if len(raw) > 8<<20 || json.Unmarshal(raw, &d) != nil {
		return v, &IntegrityError{}
	}
	canonical, _ := json.Marshal(d)
	if !bytes.Equal(canonical, raw) || d.Format != "brine-native-upload-descriptor-v1" || d.OperationID != i.original.ID || !data.ValidID(d.PointID) || d.ArtifactSHA256 != nativePointHash(artifact) || d.DescriptorSHA256 != nativePointHash(descriptor) || d.Workspace != filepath.Join("native-restores", filepath.Base(d.Workspace)) || !data.ValidID(filepath.Base(d.Workspace)) {
		return v, &IntegrityError{}
	}
	source, err := nativecapture.DecodeSource(d.Source, nativecapture.DefaultLimits())
	manifest, merr := restic.DecodeManifest(d.Manifest, restic.DefaultLimits())
	if err != nil || merr != nil || checkNativeBundleMembers(source, manifest, d.Source) != nil || manifest.Identity().CaptureID != d.PointID || manifest.Identity().BindingID != string(i.binding) {
		return v, &IntegrityError{}
	}
	permit, err := i.state.ReadEnginePermit(ctx, i.binding)
	if err != nil || manifest.Identity().EpochID != string(permit.Runtime.EpochID) {
		return v, ErrConflict
	}
	v = NativeRetainedUpload{Source: source, Manifest: manifest, Workspace: filepath.Join(string(permit.Reserved.Database.Root), d.Workspace), Directory: d.Directory, Scope: d.Scope, SourceSealSHA256: d.SourceSealSHA256, Artifact: artifact, Descriptor: descriptor}
	if source.Kind() == "initialized" {
		v.SourceEvidence, err = nativeevidence.Decode(d.SourceEvidence, nativeevidence.MaximumLimits())
		v.ConfirmedBound, merr = spacetimedb.DecodeConfirmedPairsRecord(d.ConfirmedBound)
		schema, serr := spacetimedb.DecodeDescriptorV10(descriptor, spacetimedb.DefaultSchemaLimits())
		if serr != nil {
			return NativeRetainedUpload{}, &IntegrityError{}
		}
		v.FinalPairs, serr = spacetimedb.RecoveryPairsFromPassive(v.SourceEvidence, schema)
		if err != nil || merr != nil || serr != nil || !bytes.Equal(v.FinalPairs.Bytes(), d.FinalPairs) || spacetimedb.CheckConfirmedLowerBound(v.ConfirmedBound, v.FinalPairs) != nil {
			return NativeRetainedUpload{}, &IntegrityError{}
		}
	}
	return v, nil
}

func (i *NativeBackupInspection) CheckUploadWorkspace(ctx context.Context, parent *os.Root) error {
	retained, err := i.Upload(ctx)
	if err != nil {
		return err
	}
	permit, err := i.state.ReadEnginePermit(ctx, i.binding)
	if err != nil {
		return err
	}
	relative, _ := filepath.Rel(string(permit.Reserved.Database.Root), retained.Workspace)
	identity, err := nativeUploadDirectoryIdentity(string(permit.Reserved.Database.Root), relative, parent)
	if err != nil || identity != retained.Directory {
		return ErrConflict
	}
	return nil
}

func (i *NativeBackupInspection) Material(ctx context.Context) (NativeRepositoryMaterial, error) {
	if i == nil || i.state == nil {
		return NativeRepositoryMaterial{}, ErrConflict
	}
	if err := i.check(ctx, i.state.db); err != nil {
		return NativeRepositoryMaterial{}, err
	}
	if i.original.Kind == ops.NativeBackupActivate {
		activation, err := i.Activation(ctx)
		if err != nil {
			return NativeRepositoryMaterial{}, err
		}
		metadata, err := activation.Metadata(ctx)
		return metadata.Material, err
	}
	retained, err := i.Upload(ctx)
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	material, err := readNativeRepositoryMaterial(ctx, i.state.db, restic.TransportPoint{Scope: retained.Scope})
	if material.Scope.ExpiresAt.After(i.deadline) {
		material.Scope.ExpiresAt = i.deadline
	}
	return material, err
}
func (i *NativeBackupInspection) AdmitRepository(ctx context.Context, scope restic.Scope, action restic.RepositoryAction, grant string) error {
	if action != restic.ReadRepository {
		return ErrConflict
	}
	m, err := i.Material(ctx)
	expected := m.Scope
	expected.ExpiresAt = scope.ExpiresAt
	if err != nil || expected != scope || !scope.ExpiresAt.After(time.Now().UTC()) || scope.ExpiresAt.After(m.Scope.ExpiresAt) {
		return ErrConflict
	}
	if i.original.Kind == ops.NativeBackupActivate && scope.RepositoryID == "" {
		a, err := i.Activation(ctx)
		var stage string
		stageErr := i.state.db.QueryRowContext(ctx, "SELECT stage FROM data_native_backup_initialization_events WHERE operation_id=? ORDER BY sequence DESC LIMIT 1", i.original.ID).Scan(&stage)
		if err != nil || grant != nativePointHash(a.claim) || stageErr != nil || stage != "unknown" && stage != "initialized" {
			return ErrConflict
		}
	} else if grant != "" {
		return ErrConflict
	}
	return nil
}

func (i *NativeBackupInspection) Review(ctx context.Context, observationSHA256 string) (nativecapture.BackupResolutionReview, error) {
	if i.state.readOnly || i.resolver != "" || !data.ValidCatalogHash(observationSHA256) {
		return nativecapture.BackupResolutionReview{}, ErrConflict
	}
	tx, cancel, err := i.state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nativecapture.BackupResolutionReview{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = i.check(ctx, tx); err != nil {
		return nativecapture.BackupResolutionReview{}, err
	}
	now := time.Now().UTC()
	r := nativecapture.BackupResolutionReview{Format: "brine-native-backup-resolution-review-v1", OriginalOperationID: i.original.ID, OriginalKind: string(i.original.Kind), Requester: i.original.Requester, BindingID: string(i.binding), OriginalSHA256: i.originalSHA256, ObservationSHA256: observationSHA256, IssuedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
	identity, _ := json.Marshal(r)
	r.ID = nativePointHash(identity)
	raw, _ := json.Marshal(r)
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_backup_resolutions VALUES(?,?,?)", r.ID, i.original.ID, raw)
	if err != nil {
		return nativecapture.BackupResolutionReview{}, err
	}
	return r, tx.Commit()
}
func readNativeBackupResolutionReview(ctx context.Context, q dataQuerier, id string) (nativecapture.BackupResolutionReview, error) {
	var r nativecapture.BackupResolutionReview
	var raw []byte
	var original string
	if err := q.QueryRowContext(ctx, "SELECT operation_id,canonical FROM data_native_backup_resolutions WHERE id=?", id).Scan(&original, &raw); err != nil {
		return r, err
	}
	if len(raw) > 16384 || json.Unmarshal(raw, &r) != nil {
		return r, &IntegrityError{}
	}
	canonical, _ := json.Marshal(r)
	value := r
	value.ID = ""
	identity, _ := json.Marshal(value)
	if !bytes.Equal(raw, canonical) || r.Format != "brine-native-backup-resolution-review-v1" || r.ID != id || id != nativePointHash(identity) || r.OriginalOperationID != original || !r.ExpiresAt.Equal(r.IssuedAt.Add(10*time.Minute)) || r.IssuedAt.Location() != time.UTC || r.ExpiresAt.Location() != time.UTC {
		return r, &IntegrityError{}
	}
	return r, nil
}
func (s *Store) ReadNativeBackupResolutionReview(ctx context.Context, id string) (nativecapture.BackupResolutionReview, error) {
	return readNativeBackupResolutionReview(ctx, s.db, id)
}
func (s *Store) ClaimNativeBackupResolution(ctx context.Context, operation string) (*NativeBackupInspection, error) {
	op, err := s.GetOperation(ctx, operation)
	if s.readOnly || err != nil || op.Kind != ops.NativeBackupResolve || op.State != ops.Preflight {
		return nil, ErrConflict
	}
	r, err := readNativeBackupResolutionReview(ctx, s.db, op.SecretRef)
	if err != nil || r.OriginalOperationID != op.RecoveryOf || r.Requester != op.Requester || !time.Now().UTC().Before(r.ExpiresAt) {
		return nil, ErrConflict
	}
	i, err := s.OpenNativeBackupInspection(ctx, op.RecoveryOf, op.Requester)
	if err != nil {
		return nil, err
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		_ = i.Close()
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	raw, _ := json.Marshal(r)
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_backup_resolution_events VALUES(?,'claimed',?)", operation, raw)
	if err == nil {
		i.resolver = operation
		i.review = r
		i.deadline = r.ExpiresAt
		err = i.check(ctx, tx)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		_ = i.Close()
		return nil, err
	}
	return i, nil
}

func (i *NativeBackupInspection) Complete(ctx context.Context, observationSHA256 string, originalReceipt json.RawMessage) (nativecapture.BackupResolutionReceipt, error) {
	var zero nativecapture.BackupResolutionReceipt
	if i.resolver == "" || observationSHA256 != i.review.ObservationSHA256 {
		return zero, ErrConflict
	}
	// Only the original producer's closed typed receipt has a representation here.
	if _, err := ops.DecodeTaskReceipt(i.original, originalReceipt); err != nil {
		return zero, err
	}
	material, err := i.Material(ctx)
	if err != nil {
		return zero, err
	}
	tx, cancel, err := i.state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = i.check(ctx, tx); err != nil {
		return zero, err
	}
	if err = checkNativeResolvedProducer(ctx, tx, i.original, originalReceipt); err != nil {
		return zero, err
	}
	proof := nativePointHash(originalReceipt)
	r := nativecapture.BackupResolutionReceipt{Kind: string(ops.NativeBackupResolve), OperationID: i.resolver, ReviewID: i.review.ID, OriginalOperationID: i.original.ID, OriginalKind: string(i.original.Kind), BindingID: string(i.binding), EpochID: material.Scope.EpochID, OriginalSHA256: i.originalSHA256, ObservationSHA256: observationSHA256, CompletionSHA256: proof, Receipt: originalReceipt, Outcome: "adopted"}
	raw, _ := json.Marshal(r)
	if _, err = nativecapture.DecodeBackupResolutionReceipt(raw); err != nil {
		return zero, err
	}
	for _, stage := range []string{"observed", "completed"} {
		if _, err = tx.ExecContext(ctx, "INSERT INTO data_native_backup_resolution_events VALUES(?,?,?)", i.resolver, stage, raw); err != nil {
			return zero, err
		}
	}
	return r, tx.Commit()
}

func readNativeBackupResolvedReceipt(ctx context.Context, q dataQuerier, original ops.Operation) (json.RawMessage, error) {
	var id string
	if err := q.QueryRowContext(ctx, "SELECT id FROM operations WHERE kind='native_backup_resolve' AND recovery_of=? AND state='succeeded'", original.ID).Scan(&id); err != nil {
		return nil, ErrConflict
	}
	op, err := nativeResolutionOperation(ctx, q, id)
	if err != nil || original.State != ops.RecoveryRequired || op.Requester != original.Requester || op.App != original.App {
		return nil, ErrConflict
	}
	var outcomeRaw, completionRaw []byte
	if err = q.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", id).Scan(&outcomeRaw); err != nil {
		return nil, err
	}
	outcome, err := ops.DecodeTaskOutcome(op, outcomeRaw)
	r, decodeErr := nativecapture.DecodeBackupResolutionReceipt(outcome.Receipt)
	if err != nil || decodeErr != nil || r.OriginalKind != string(original.Kind) || r.CompletionSHA256 != nativePointHash(r.Receipt) {
		return nil, &IntegrityError{}
	}
	if err = q.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_resolution_events WHERE operation_id=? AND stage='completed'", id).Scan(&completionRaw); err != nil || !bytes.Equal(completionRaw, outcome.Receipt) {
		return nil, &IntegrityError{}
	}
	review, err := readNativeBackupResolutionReview(ctx, q, op.SecretRef)
	_, binding, hash, originalErr := nativeBackupOriginal(ctx, q, original.ID, original.Requester)
	if err != nil || originalErr != nil || r.OriginalSHA256 != hash || r.OriginalSHA256 != review.OriginalSHA256 || r.ObservationSHA256 != review.ObservationSHA256 || r.BindingID != string(binding) {
		return nil, &IntegrityError{}
	}
	if _, err = ops.DecodeTaskReceipt(original, r.Receipt); err != nil {
		return nil, errors.Join(&IntegrityError{}, err)
	}
	return r.Receipt, nil
}

func checkNativeBackupResolutionIntent(ctx context.Context, q dataQuerier, intent ops.Intent, requester string) error {
	review, err := readNativeBackupResolutionReview(ctx, q, intent.SecretRef)
	if err != nil || review.OriginalOperationID != intent.RecoveryOf || review.Requester != requester || !time.Now().UTC().Before(review.ExpiresAt) {
		return ErrConflict
	}
	original, binding, hash, err := nativeBackupOriginal(ctx, q, intent.RecoveryOf, requester)
	if err != nil || original.App != intent.App || string(binding) != review.BindingID || hash != review.OriginalSHA256 || string(original.Kind) != review.OriginalKind {
		return ErrConflict
	}
	return nil
}
