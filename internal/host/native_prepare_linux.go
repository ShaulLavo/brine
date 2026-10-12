//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativePrepareWork struct {
	runtime     nativeBackupRuntime
	input       store.NativeTaskInput
	operation   ops.Operation
	fence       data.QuiescenceFence
	preparation *store.EnginePreparation
	lifetime    *data.EngineLock
	native      *store.NativePreparation
	stopped     *store.NativeStoppedPreparation
	permit      store.EnginePermit
	copied      *engineSourceCopy
	parent      *os.Root
	path        string
	module      nativeCurrentModule
	confirmed   spacetimedb.ConfirmedPairsRecord
	observed    nativeOwnedObservation
	withdrawn   caddy.State
	prior       caddy.State
}

func nativePrepareTask(service Service, stateRoot, home string, session nativeBackupRuntime) jobs.TaskHandler {
	return func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		input, err := service.Store.ReadNativeTaskInput(ctx, op.ID)
		if err != nil || input.Kind != ops.NativePrepare {
			return nil, result.New(result.PolicyRefused, nil)
		}
		backup, err := service.Store.ReadNativeBackupPolicy(ctx, input.BindingID)
		pol, policyErr := service.Policy.Load(ctx)
		if err != nil || policyErr != nil || pol.Hash() != "sha256:"+backup.PolicySHA256 {
			return nil, result.New(result.PolicyRefused, nil)
		}
		ctx, cancel := context.WithTimeout(ctx, time.Duration(backup.Backup.MaxMaintenanceSeconds)*time.Second)
		defer cancel()
		lock, err := service.Store.AcquireHostLock(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = lock.Release() }()
		work := &nativePrepareWork{runtime: session, input: input, operation: op}
		defer func() { _ = work.close() }()
		if err := work.acquire(ctx); err != nil {
			return nil, err
		}
		if err := work.withdrawIngress(ctx, service, session.routes); err != nil {
			return nil, err
		}
		if err := work.stop(ctx, service); err != nil {
			return nil, err
		}
		point, err := work.capture(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(nativecapture.TaskReceipt{Kind: string(ops.NativePrepare), OperationID: op.ID, BindingID: string(input.BindingID), EpochID: string(point.EpochID), PointID: point.PointID, PointSHA256: point.PointSHA256, Outcome: "verified"})
	}
}

func (w *nativePrepareWork) acquire(ctx context.Context) error {
	permit, err := w.runtime.state.ReadEnginePermit(ctx, w.input.BindingID)
	if err != nil || permit.Allocation == nil {
		return errEngineSource
	}
	w.fence, err = w.runtime.state.HoldDataFence(ctx, permit.Reserved.Database.DatabaseID, w.operation.ID)
	if err != nil {
		return err
	}
	w.preparation, err = w.runtime.state.AcquireEnginePreparation(ctx, w.input.BindingID, w.fence.ID, w.operation.ID)
	if err != nil {
		return err
	}
	w.permit, err = w.runtime.state.ReadEnginePermit(ctx, w.input.BindingID)
	if err != nil {
		return err
	}
	w.native, err = w.runtime.state.BeginNativePreparation(ctx, w.preparation, w.operation.ID)
	if err != nil {
		return err
	}
	return nil
}

func (w *nativePrepareWork) stop(ctx context.Context, service Service) error {
	if w.permit.Runtime.State == data.EngineUnpublished {
		return w.stopEmpty(ctx, service)
	}
	attempt, err := bindNativeStopAttempt(ctx, w.runtime.session, w.runtime.stateRoot, w.runtime.home, w.permit)
	if err != nil {
		return err
	}
	defer attempt.close()
	material, err := readEngineMaterial(ctx, w.runtime.stateRoot, w.permit)
	if err != nil {
		return err
	}
	defer material.clear()
	w.observed, err = observeOwnedNative(ctx, attempt.native, w.permit, material)
	if err != nil {
		return err
	}
	root, err := openEngineSource(w.permit.Reserved.Database)
	if err != nil {
		return err
	}
	artifact, err := readNativeProgramArtifact(ctx, root, w.permit)
	_ = root.Close()
	if err != nil {
		return err
	}
	w.module, err = w.runtime.extractCurrentModule(ctx, w.preparation, w.permit, artifact)
	if err != nil {
		return err
	}
	w.confirmed, err = observeNativeConfirmed(ctx, w.permit, material, w.module)
	if err != nil {
		return err
	}
	err = w.runtime.state.RecordNativeStopIntent(ctx, w.native, store.NativeStopIntent{Mode: "managed_clean_stop", ContainerID: attempt.container.ID, PID: attempt.native.pid, StartTicks: attempt.native.ticks, ObservedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	if err := w.preparation.Close(); err != nil {
		return err
	}
	w.preparation = nil
	if err := attempt.stop(ctx, w.runtime.session, w.runtime.home); err != nil {
		return err
	}
	if err := w.reacquire(ctx); err != nil {
		return err
	}
	observed, err := attempt.observation(ctx, w.runtime.session)
	if err != nil {
		return errEngineSource
	}
	if err := w.verifyIngressClosed(ctx); err != nil {
		return err
	}
	observed.IngressClosed = true
	w.stopped, err = w.runtime.state.RecordNativeStoppedPreparation(ctx, w.native, w.lifetime, observed)
	return err
}

func (w *nativePrepareWork) stopEmpty(ctx context.Context, service Service) error {
	if service.managedObserver == nil || service.managedObserver.sourceAbsent(ctx, w.permit.Reserved.Database) != nil {
		return errEngineSource
	}
	if err := w.verifyIngressClosed(ctx); err != nil {
		return err
	}
	if err := w.runtime.state.RecordNativeStopIntent(ctx, w.native, store.NativeStopIntent{Mode: "unpublished_absent", ObservedAt: time.Now().UTC()}); err != nil {
		return err
	}
	var err error
	w.lifetime, err = data.AcquireEngineLifetimeLock(ctx, w.runtime.stateRoot, w.input.BindingID)
	if err != nil {
		return err
	}
	w.stopped, err = w.runtime.state.RecordNativeStoppedPreparation(ctx, w.native, w.lifetime, store.NativeStopObservation{Mode: "unpublished_absent", FinishedAt: time.Now().UTC(), SourceAbsent: true, RestartJobsAbsent: true, IngressClosed: true})
	return err
}

func (w *nativePrepareWork) reacquire(ctx context.Context) error {
	var err error
	w.preparation, err = w.runtime.state.AcquireEnginePreparation(ctx, w.input.BindingID, w.fence.ID, w.operation.ID)
	if err != nil {
		return err
	}
	w.lifetime, err = data.AcquireEngineLifetimeLock(ctx, w.runtime.stateRoot, w.input.BindingID)
	if err != nil {
		return err
	}
	w.native, err = w.runtime.state.BeginNativePreparation(ctx, w.preparation, w.operation.ID)
	return err
}

func (w *nativePrepareWork) capture(ctx context.Context) (store.NativePreparedPointView, error) {
	parent, path, err := newNativePreparedRestoreParent(ctx, w.permit)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	w.parent, w.path = parent, path
	copyID, err := data.NewID()
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	w.copied, err = copyEngineSource(ctx, w.permit.Reserved.Database, path, copyID, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	if w.permit.Runtime.State == data.EngineUnpublished {
		return w.captureEmpty(ctx)
	}
	replica, err := nativeSoleReplica(w.copied.original)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	checkpoint, err := buildNativeCheckpoint(ctx, w.runtime.state, w.runtime.stateRoot, w.stopped, w.copied, w.observed, replica)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	checkpoint, err = w.sealedRecoveryCheckpoint(ctx, checkpoint)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	inventory, err := nativeOwnedInventory(ctx, w.copied.source, w.copied.original, string(w.input.BindingID), string(w.permit.Runtime.EpochID), w.stopped.PointID())
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	selection, err := nativecapture.Select(inventory, nativecapture.DefaultLimits())
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	source, err := nativecapture.InitializedSource(checkpoint.checkpoint, selection, nativecapture.DefaultLimits())
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	nonce, err := data.NewID()
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	view, err := materializeNativeSelection(ctx, w.copied.root, path, selection, nonce)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	defer func() { _ = view.close() }()
	pin, err := readNativeReaderPin(ctx)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	witness, err := readNativeStoppedWitness(ctx, w.runtime.session, pin, view, checkpoint)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	if err := view.close(); err != nil {
		return store.NativePreparedPointView{}, err
	}
	final, err := spacetimedb.RecoveryPairsFromPassive(witness.evidence, w.module.candidate.candidate.Schema())
	if err != nil || spacetimedb.CheckConfirmedLowerBound(w.confirmed, final) != nil {
		return store.NativePreparedPointView{}, errEngineSource
	}
	return w.uploadAndComplete(ctx, source, w.copied.source, witness.evidence, final)
}

func (w *nativePrepareWork) captureEmpty(ctx context.Context) (store.NativePreparedPointView, error) {
	empty, err := buildNativeEmptySource(ctx, w.runtime.state, w.runtime.stateRoot, w.stopped, w.copied)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	empty, err = w.sealedRecoveryEmpty(ctx, empty)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	lease, err := acquireInitialPublisherStage(ctx, empty, podman.New(w.runtime.session))
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	initial, err := runInitialPublisherStage(ctx, lease, localexec.ExecRunner{}, w.runtime.session)
	if err != nil || retainNativeInitialOwner(ctx, initial) != nil {
		return store.NativePreparedPointView{}, errEngineSource
	}
	return w.uploadAndComplete(ctx, empty.source, w.copied.source, nativeevidence.Evidence{}, spacetimedb.RecoveredPairs{})
}

func (w *nativePrepareWork) close() error {
	var err error
	if w.copied != nil {
		err = w.copied.discard()
		w.copied = nil
	}
	if w.parent != nil {
		err = errors.Join(err, w.parent.Close())
		w.parent = nil
	}
	if w.lifetime != nil {
		err = errors.Join(err, w.lifetime.Release())
		w.lifetime = nil
	}
	if w.preparation != nil {
		err = errors.Join(err, w.preparation.Close())
		w.preparation = nil
	}
	return err
}

func (w *nativePrepareWork) uploadAndComplete(ctx context.Context, source nativecapture.Source, original *os.Root, evidence nativeevidence.Evidence, final spacetimedb.RecoveredPairs) (store.NativePreparedPointView, error) {
	admission, err := w.runtime.state.NativePreparedCaptureRepository(ctx, w.stopped)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	material, err := admission.Material(ctx)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	ctx, cancel, err := nativeTransportContext(ctx, material.Scope)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	defer cancel()
	client, repository, err := w.runtime.repositoryMaterial(ctx, material, admission, "")
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	bundle, err := materializeNativeBundle(ctx, original, source, w.parent, w.path, w.stopped.PointID(), w.runtime.stateRoot, w.permit)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	defer func() { _ = bundle.Close() }()
	if err := w.runtime.state.RecordNativePreparedUploadIntent(ctx, w.stopped, source, bundle.Manifest()); err != nil {
		return store.NativePreparedPointView{}, err
	}
	transport, err := client.Backup(ctx, repository, w.path, bundle)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	extracted, err := client.RestoreExact(ctx, repository, w.path, w.parent, "download", transport, bundle.Manifest())
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	defer func() { _ = extracted.Root().Close() }()
	observation := store.NativePreparedObservation{Parent: w.parent, Source: source, Manifest: bundle.Manifest(), Transport: transport, SourceSealSHA256: w.copied.original.hash, SourceEvidence: evidence, ConfirmedBound: w.confirmed, FinalPairs: final, Artifact: w.module.artifact.Bytes(), Descriptor: w.module.descriptor}
	if checkpoint, selection, initialized := source.Initialized(); initialized {
		restored, disposal, err := w.runtime.verifyExtracted(ctx, extracted, w.parent, w.path, checkpoint, selection, evidence, w.module.candidate.candidate.Schema(), w.confirmed, final)
		if err != nil {
			return store.NativePreparedPointView{}, err
		}
		observation.RestoredEvidence = restored
		observation.ReaderNonce, observation.ReaderViewSHA256, observation.ReaderImageID, observation.ReaderDisposedAt = disposal.nonce, disposal.viewHash, disposal.imageID, disposal.at
	}
	if w.copied.sourceUnchanged(ctx) != nil {
		return store.NativePreparedPointView{}, errEngineSource
	}
	return w.runtime.state.CompleteNativePreparedPoint(ctx, w.stopped, extracted, observation)
}
