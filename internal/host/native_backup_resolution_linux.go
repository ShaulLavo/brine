//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativeBackupResolutionOperations struct {
	service Service
	jobs    jobs.Service
	runtime nativeBackupRuntime
}

func (o nativeBackupResolutionOperations) InspectBackup(ctx context.Context, args dispatch.NativeBackupInspectArgs) (dispatch.NativeBackupInspectionView, error) {
	lock, lockErr := o.lock(ctx)
	if lockErr != nil {
		return dispatch.NativeBackupInspectionView{}, lockErr
	}
	defer func() { _ = lock.Release() }()
	inspection, err := o.service.Store.OpenNativeBackupInspection(ctx, args.OperationID, o.jobs.Requester)
	if err != nil {
		return dispatch.NativeBackupInspectionView{}, err
	}
	defer func() { _ = inspection.Close() }()
	observed, err := o.inspect(ctx, inspection)
	if err != nil {
		return dispatch.NativeBackupInspectionView{}, err
	}
	defer func() { _ = observed.close(ctx) }()
	return observed.view(inspection), nil
}
func (o nativeBackupResolutionOperations) ReviewBackup(ctx context.Context, args dispatch.NativeBackupReviewArgs) (review nativecapture.BackupResolutionReview, reviewErr error) {
	lock, lockErr := o.lock(ctx)
	if lockErr != nil {
		return review, lockErr
	}
	defer func() { _ = lock.Release() }()
	inspection, err := o.service.Store.OpenNativeBackupInspection(ctx, args.OperationID, o.jobs.Requester)
	if err != nil {
		return review, err
	}
	defer func() { reviewErr = errors.Join(reviewErr, inspection.Close()) }()
	observed, err := o.inspect(ctx, inspection)
	if err != nil {
		return review, err
	}
	defer func() { reviewErr = errors.Join(reviewErr, observed.close(ctx)) }()
	return inspection.Review(ctx, observed.hash())
}
func (o nativeBackupResolutionOperations) ResolveBackup(ctx context.Context, args dispatch.NativeBackupResolveArgs) (jobs.Accepted, error) {
	review, err := o.service.Store.ReadNativeBackupResolutionReview(ctx, args.ReviewID)
	if err != nil || review.Requester != o.jobs.Requester {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	original, err := o.service.Store.GetOperation(ctx, review.OriginalOperationID)
	if err != nil {
		return jobs.Accepted{}, err
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: ops.NativeBackupResolve, App: original.App, SecretRef: review.ID, RecoveryOf: original.ID}, args.IdempotencyKey)
}
func (o nativeBackupResolutionOperations) task(ctx context.Context, op ops.Operation) (receipt json.RawMessage, taskErr error) {
	lock, lockErr := o.lock(ctx)
	if lockErr != nil {
		return nil, lockErr
	}
	defer func() { _ = lock.Release() }()
	inspection, err := o.service.Store.ClaimNativeBackupResolution(ctx, op.ID)
	if err != nil {
		return nil, err
	}
	defer func() { taskErr = errors.Join(taskErr, inspection.Close()) }()
	observed, err := o.inspect(ctx, inspection)
	if err != nil {
		return nil, err
	}
	defer func() { taskErr = errors.Join(taskErr, observed.close(ctx)) }()
	if err = inspection.CheckReviewedObservation(ctx, observed.hash()); err != nil {
		return nil, err
	}
	originalReceipt, err := o.adopt(ctx, inspection, observed)
	if err != nil {
		return nil, err
	}
	completed, err := inspection.Complete(ctx, observed.hash(), originalReceipt)
	if err != nil {
		return nil, err
	}
	if inspection.OriginalKind() != ops.NativeBackupActivate {
		if err = o.cleanupOriginalUpload(ctx, observed); err != nil {
			return nil, err
		}
	}
	return json.Marshal(completed)
}

type nativeBackupObserved struct {
	material       store.NativeRepositoryMaterial
	runtime        nativeBackupRuntime
	inspection     *store.NativeBackupInspection
	workspaceID    string
	lifetime       *data.EngineLock
	preparedStop   store.NativeStopObservation
	retained       store.NativeRetainedUpload
	originalParent *os.Root
	parent         *os.Root
	path           string
	extracted      restic.AuthenticatedRestore
	observation    store.NativePreparedObservation
	initialized    restic.InitializedRepository
	activation     store.NativeBackupActivationMetadata
	repositoryID   string
	configSHA256   string
	unitsState     string
}

func (v *nativeBackupObserved) hash() string {
	point := v.extracted.Point()
	// Credential expiry governs the read, not the identity of the inspected effect.
	point.Scope.ExpiresAt = time.Time{}
	raw, _ := json.Marshal(struct {
		RepositoryID   string                `json:"repository_id"`
		ConfigSHA256   string                `json:"config_sha256"`
		UnitsState     string                `json:"units_state"`
		Point          restic.TransportPoint `json:"point"`
		SourceSHA256   string                `json:"source_sha256"`
		ManifestSHA256 string                `json:"manifest_sha256"`
		NativeSHA256   string                `json:"native_sha256"`
	}{v.repositoryID, v.configSHA256, v.unitsState, point, v.retained.Source.SHA256(), v.retained.Manifest.SHA256(), v.observation.RestoredEvidence.SHA256()})
	return nativeBackupBytesHash(raw)
}
func (v *nativeBackupObserved) view(i *store.NativeBackupInspection) dispatch.NativeBackupInspectionView {
	return dispatch.NativeBackupInspectionView{OperationID: i.OriginalOperationID(), Kind: string(i.OriginalKind()), BindingID: string(i.BindingID()), EpochID: v.material.Scope.EpochID, ObservationSHA256: v.hash(), State: "verified_remote"}
}
func (v *nativeBackupObserved) close(ctx context.Context) error {
	var err error
	if v.lifetime != nil {
		err = v.lifetime.Release()
		v.lifetime = nil
	}
	if root := v.extracted.Root(); root != nil {
		err = errors.Join(err, root.Close())
	}
	if v.originalParent != nil {
		err = errors.Join(err, v.originalParent.Close())
		v.originalParent = nil
	}
	if v.parent != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jobs.JournalTimeout)
		cleanupErr := nativeUploadProcessesAbsent(ctx, v.path)
		if cleanupErr == nil {
			cleanupErr = (nativeBackupResolutionOperations{runtime: v.runtime}).disposeOriginalUploadReaders(ctx, v.path)
		}
		if cleanupErr == nil {
			cleanupErr = discardNativeWorkspace(v.parent, v.path)
		}
		if cleanupErr != nil {
			_ = v.parent.Close()
		}
		err = errors.Join(err, cleanupErr)
		if cleanupErr == nil && v.workspaceID != "" {
			err = errors.Join(err, v.inspection.DisposeWorkspace(ctx, v.workspaceID))
		}
		cancel()
		v.parent = nil
	}
	return err
}
func (o nativeBackupResolutionOperations) inspect(ctx context.Context, i *store.NativeBackupInspection) (observed *nativeBackupObserved, inspectErr error) {
	v := &nativeBackupObserved{inspection: i, runtime: o.runtime}
	complete := false
	defer func() {
		if !complete {
			inspectErr = errors.Join(inspectErr, v.close(ctx))
		}
	}()
	material, err := i.Material(ctx)
	pol, policyErr := o.service.Policy.Load(ctx)
	if err != nil || policyErr != nil || pol.Hash() != material.CredentialRecord.PolicyHash {
		return nil, result.New(result.PolicyRefused, nil)
	}
	v.material = material
	if i.OriginalKind() == ops.NativePrepare {
		if err = o.inspectPreparedOriginal(ctx, i, v); err != nil {
			return nil, err
		}
	}
	permit, err := o.service.Store.ReadEnginePermit(ctx, i.BindingID())
	if err != nil {
		return nil, err
	}
	if err = o.cleanupInspectionWorkspaces(ctx, i, permit); err != nil {
		return nil, err
	}
	v.parent, v.path, err = newNativePreparedRestoreParent(ctx, permit)
	if err != nil {
		return nil, err
	}
	v.workspaceID, err = i.RecordWorkspace(ctx, v.parent, v.path)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := nativeTransportContext(ctx, material.Scope)
	if err != nil {
		return nil, err
	}
	defer cancel()
	grant := ""
	if i.OriginalKind() == ops.NativeBackupActivate {
		activation, err := i.Activation(ctx)
		if err != nil {
			return nil, err
		}
		v.activation, err = activation.Metadata(ctx)
		if err != nil {
			return nil, err
		}
		if material.Scope.RepositoryID == "" {
			grant = v.activation.CreationGrantSHA256
		}
	}
	client, repository, err := o.runtime.repositoryMaterial(ctx, material, i, grant)
	if err != nil {
		return nil, err
	}
	if i.OriginalKind() == ops.NativeBackupActivate {
		err = o.inspectActivation(ctx, i, v, client, repository, grant)
	} else {
		err = o.inspectUpload(ctx, i, v, client, repository)
	}
	if err != nil {
		return nil, err
	}
	complete = true
	return v, nil
}
func (o nativeBackupResolutionOperations) verifyUnits(ctx context.Context, metadata store.NativeBackupActivationMetadata) error {
	root, err := engineOpenRoot(o.runtime.home)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	unit, timer := nativeBackupUnitBytes(metadata)
	name := nativeBackupUnitName(metadata)
	if verifyNativeBackupUnit(ctx, root, name+".service", unit) != nil || verifyNativeBackupUnit(ctx, root, name+".timer", timer) != nil {
		return errEngineSource
	}
	observed, err := o.runtime.session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "show", name + ".timer", "--no-pager", "--property=LoadState", "--property=ActiveState", "--property=UnitFileState", "--property=FragmentPath"}, nil, false)
	if err != nil {
		return err
	}
	return checkNativeBackupTimerProperties(observed.Stdout, map[string]string{"LoadState": "loaded", "ActiveState": "active", "UnitFileState": "enabled", "FragmentPath": filepath.Join(o.runtime.home, ".config/systemd/user", name+".timer")})
}
func (o nativeBackupResolutionOperations) adopt(ctx context.Context, i *store.NativeBackupInspection, v *nativeBackupObserved) (json.RawMessage, error) {
	if i.OriginalKind() == ops.NativeBackupActivate {
		return o.adoptActivation(ctx, i, v)
	}
	if i.OriginalKind() == ops.NativePrepare {
		return o.adoptPrepared(ctx, i, v)
	}
	capture, err := i.Capture(ctx)
	if err != nil {
		return nil, err
	}
	point, err := capture.Complete(ctx, v.extracted, v.observation)
	if err != nil {
		return nil, err
	}
	backup, err := o.service.Store.ReadNativeBackupPolicy(ctx, point.BindingID)
	checkpoint, _, _ := v.retained.Source.Initialized()
	if err != nil {
		return nil, err
	}
	c := checkpoint.Record()
	return json.Marshal(nativecapture.TaskReceipt{Kind: string(ops.NativeCapture), OperationID: i.OriginalOperationID(), BindingID: string(point.BindingID), EpochID: string(point.EpochID), PointID: point.PointID, PointSHA256: point.PointSHA256, Outcome: "verified", Engine: "spacetimedb", ModuleSHA256: c.ArtifactSHA256, SchemaSHA256: c.SchemaSHA256, CaptureIntervalSeconds: backup.Backup.CaptureIntervalSeconds, Loss: "unknown"})
}

func (o nativeBackupResolutionOperations) inspectActivation(ctx context.Context, i *store.NativeBackupInspection, v *nativeBackupObserved, client *restic.Client, repository restic.Repository, grant string) error {
	var err error
	if grant != "" {
		v.initialized, err = client.InspectInitialization(ctx, repository, v.path)
		if err != nil {
			return err
		}
		v.repositoryID = v.initialized.Scope().RepositoryID
		v.configSHA256 = v.initialized.ConfigurationSHA256()
	} else {
		v.repositoryID, err = client.RepositoryIdentity(ctx, repository, v.path)
		if err != nil {
			return err
		}
	}
	stage, err := i.ActivationStage(ctx)
	if err != nil {
		return err
	}
	if stage == "prepared" {
		v.unitsState = "not_issued"
	} else {
		if err = o.verifyUnits(ctx, v.activation); err != nil {
			return err
		}
		v.unitsState = "verified"
	}
	return nil
}

func (o nativeBackupResolutionOperations) inspectUpload(ctx context.Context, i *store.NativeBackupInspection, v *nativeBackupObserved, client *restic.Client, repository restic.Repository) error {
	var err error
	v.retained, err = i.Upload(ctx)
	if err != nil {
		return err
	}
	point, known, err := i.CompletedTransport(ctx)
	if err != nil {
		return err
	}
	if _, statErr := os.Lstat(v.retained.Workspace); statErr == nil {
		v.originalParent, err = engineOpenRoot(v.retained.Workspace)
		if err != nil || i.CheckUploadWorkspace(ctx, v.originalParent) != nil {
			return errEngineSource
		}
	} else if !os.IsNotExist(statErr) || !known {
		return errEngineSource
	}
	if known {
		point.Scope = v.material.Scope
		if err = client.InspectExact(ctx, repository, v.path, point); err != nil {
			return err
		}
	} else {
		bundle, err := restic.Seal(ctx, filepath.Join(v.retained.Workspace, "bundle"), v.path, v.retained.Manifest.Identity(), restic.DefaultLimits())
		if err != nil {
			return err
		}
		defer func() { _ = bundle.Close() }()
		if !bytes.Equal(bundle.Manifest().Bytes(), v.retained.Manifest.Bytes()) {
			return errEngineSource
		}
		point, err = client.InspectCapture(ctx, repository, v.path, bundle)
		if err != nil {
			return err
		}
	}
	v.extracted, err = client.RestoreExact(ctx, repository, v.path, v.parent, "download", point, v.retained.Manifest)
	if err != nil {
		return err
	}
	v.observation = store.NativePreparedObservation{Parent: v.parent, Source: v.retained.Source, Manifest: v.retained.Manifest, Transport: point, SourceSealSHA256: v.retained.SourceSealSHA256, SourceEvidence: v.retained.SourceEvidence, ConfirmedBound: v.retained.ConfirmedBound, FinalPairs: v.retained.FinalPairs, Artifact: v.retained.Artifact, Descriptor: v.retained.Descriptor}
	if checkpoint, selection, initialized := v.retained.Source.Initialized(); initialized {
		schema, err := spacetimedb.DecodeDescriptorV10(v.retained.Descriptor, spacetimedb.DefaultSchemaLimits())
		if err != nil {
			return err
		}
		evidence, disposal, err := o.runtime.verifyExtracted(ctx, v.extracted, v.parent, v.path, checkpoint, selection, v.retained.SourceEvidence, schema, v.retained.ConfirmedBound, v.retained.FinalPairs)
		if err != nil {
			return err
		}
		v.observation.RestoredEvidence = evidence
		v.observation.ReaderNonce = disposal.nonce
		v.observation.ReaderViewSHA256 = disposal.viewHash
		v.observation.ReaderImageID = disposal.imageID
		v.observation.ReaderDisposedAt = disposal.at
	}
	if v.originalParent != nil {
		if err = i.CheckUploadWorkspace(ctx, v.originalParent); err != nil {
			return err
		}
	}
	return nil
}

func (o nativeBackupResolutionOperations) adoptActivation(ctx context.Context, i *store.NativeBackupInspection, v *nativeBackupObserved) (json.RawMessage, error) {
	activation, err := i.Activation(ctx)
	if err != nil {
		return nil, err
	}
	if v.activation.Previous.Revision == 0 {
		if err = activation.RecordInitialized(ctx, v.initialized); err != nil {
			return nil, err
		}
	}
	unit, timer := nativeBackupUnitBytes(v.activation)
	if v.unitsState == "not_issued" {
		if err = i.BeginResolutionUnits(ctx); err != nil {
			return nil, err
		}
		if err = activation.BeginUnits(ctx); err != nil {
			return nil, err
		}
		if err = installNativeBackupUnits(ctx, o.runtime.home, o.runtime.session, v.activation, unit, timer); err != nil {
			return nil, err
		}
	}
	if err = o.verifyUnits(ctx, v.activation); err != nil {
		return nil, err
	}
	receipt, err := activation.Commit(ctx, store.NativeBackupActivationObservation{RepositoryID: v.repositoryID, UnitSHA256: nativeBackupBytesHash(unit), TimerSHA256: nativeBackupBytesHash(timer), RecoveryDeliveryID: v.material.RecoveryDeliveryID})
	if err != nil {
		return nil, err
	}
	return json.Marshal(receipt)
}
