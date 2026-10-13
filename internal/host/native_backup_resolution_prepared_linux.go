//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/store"
	"os"
	"time"
)

func (o nativeBackupResolutionOperations) inspectPreparedOriginal(ctx context.Context, i *store.NativeBackupInspection, v *nativeBackupObserved) error {
	expected, err := i.PreparedExpectation(ctx)
	if err != nil {
		return err
	}
	v.lifetime, err = data.AcquireEngineLifetimeLock(ctx, o.runtime.stateRoot, i.BindingID())
	if err != nil {
		return err
	}
	manager, current, previous, err := o.runtime.preparedObservedRouteStates(ctx, o.service, expected.Routes)
	if err != nil {
		return err
	}
	v.preparedStop, err = o.runtime.inspectPreparedStop(ctx, expected.Routes.App, expected.Permit, expected.Stop, manager, current, previous)
	if err != nil {
		return err
	}
	source, err := openEngineSource(expected.Permit.Reserved.Database)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	if expected.NeedsCapture {
		if err := o.inspectStoppedUnpublished(ctx, i, v, expected, source); err != nil {
			return err
		}
		return nil
	}
	retained, err := i.Upload(ctx)
	if err != nil {
		return err
	}
	seal, err := sealOwnedEngineSource(ctx, source, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil || seal.hash != retained.SourceSealSHA256 {
		return errEngineSource
	}
	return nil
}
func (o nativeBackupResolutionOperations) adoptPrepared(ctx context.Context, i *store.NativeBackupInspection, v *nativeBackupObserved) (json.RawMessage, error) {
	if v.lifetime == nil {
		return nil, errEngineSource
	}
	stopped, err := i.ResumePrepared(ctx, v.lifetime, v.preparedStop)
	if err != nil {
		return nil, err
	}
	var point store.NativePreparedPointView
	if v.needsCapture {
		point, err = o.continueStoppedUnpublished(ctx, i, v, stopped)
	} else {
		point, err = o.service.Store.CompleteNativePreparedPoint(ctx, stopped, v.extracted, v.observation)
	}

	if err != nil {
		return nil, err
	}
	backup, err := o.service.Store.ReadNativeBackupPolicy(ctx, point.BindingID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(nativecapture.TaskReceipt{Kind: string(ops.NativePrepare), OperationID: i.OriginalOperationID(), BindingID: string(point.BindingID), EpochID: string(point.EpochID), PointID: point.PointID, PointSHA256: point.PointSHA256, Outcome: "verified", Engine: "spacetimedb", ModuleSHA256: point.ArtifactWasmSHA256, SchemaSHA256: point.SchemaSHA256, CaptureIntervalSeconds: backup.Backup.CaptureIntervalSeconds, Loss: "unknown"})
}

// The host generation remains fixed through readback and resolution. The lock
// precedes binding/lifetime guards, matching normal preparation ordering.
func (o nativeBackupResolutionOperations) lock(ctx context.Context) (ops.Lock, error) {
	return o.service.Store.AcquireHostLock(ctx)
}

func (o nativeBackupResolutionOperations) inspectStoppedUnpublished(ctx context.Context, i *store.NativeBackupInspection, v *nativeBackupObserved, expected store.NativeBackupPreparedExpectation, source *os.Root) error {
	if expected.Permit.Runtime.State != data.EngineUnpublished || expected.Stop.Mode != "unpublished_absent" || o.service.managedObserver == nil || o.service.managedObserver.sourceAbsent(ctx, expected.Permit.Reserved.Database) != nil {
		return errEngineSource
	}
	seal, err := sealOwnedEngineSource(ctx, source, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil || expected.Permit.Allocation == nil || seal.root.Device != expected.Permit.Allocation.DirectoryDevice || seal.root.Inode != expected.Permit.Allocation.DirectoryInode || !engineModuleHTTPDisabled(source) {
		return errEngineSource
	}
	_, config, err := nativeEmptyMembers(seal)
	preparation, prepErr := o.service.Store.ReadManagedPreparation(ctx, i.BindingID())
	if err != nil || prepErr != nil || config != preparation.ConfigSHA256 || observeNativeWorkspaceAbsent(ctx, source) != nil {
		return errEngineSource
	}
	material, err := readEngineMaterial(ctx, o.runtime.stateRoot, expected.Permit)
	if err != nil {
		return err
	}
	material.clear()
	if err := i.RequireInitialStagesSettled(ctx); err != nil {
		return err
	}
	stages, err := i.InitialStages(ctx)
	if err != nil {
		return err
	}
	stagesRaw, _ := json.Marshal(stages)
	previousClaims, err := i.PreviousResolutionClaimsSHA256(ctx)
	if err != nil {
		return err
	}
	parentHash, err := inspectNativeStageParentAbsent(ctx, string(expected.Permit.Reserved.Database.Root))
	if err != nil {
		return err
	}
	// Initial-stage journal absence has no authority. The complete protected
	// parent and every process/mount/descriptor context were observed above.
	v.needsCapture = true
	v.preparedPermit = expected.Permit
	v.sourceSealSHA256 = seal.hash
	v.stageParentSHA256 = parentHash
	v.initialStagesSHA256 = nativeBackupBytesHash(stagesRaw)
	v.previousClaimsSHA256 = previousClaims
	v.unitsState = "absent"
	return nil
}

func inspectNativeStageParentAbsent(ctx context.Context, rootPath string) (string, error) {
	base, err := engineOpenRoot(rootPath)
	if err != nil {
		return "", errEngineSource
	}
	defer func() { _ = base.Close() }()
	if engineDirectoryChain(base, "native-restores") != nil {
		return "", errEngineSource
	}
	parent, err := base.OpenRoot("native-restores")
	if err != nil {
		return "", errEngineSource
	}
	defer func() { _ = parent.Close() }()
	seal, err := sealEngineRoot(ctx, parent, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil || len(seal.members) != 0 || observeNativeWorkspaceAbsent(ctx, parent) != nil {
		return "", errEngineSource
	}
	after, err := sealEngineRoot(ctx, parent, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil || after.hash != seal.hash {
		return "", errEngineSource
	}
	return seal.hash, nil
}

func (o nativeBackupResolutionOperations) continueStoppedUnpublished(ctx context.Context, i *store.NativeBackupInspection, v *nativeBackupObserved, stopped *store.NativeStoppedPreparation) (point store.NativePreparedPointView, err error) {
	input, err := o.service.Store.ReadNativeTaskInput(ctx, i.OriginalOperationID())
	if err != nil || input.Kind != ops.NativePrepare || !v.needsCapture {
		return store.NativePreparedPointView{}, errEngineSource
	}
	original, err := o.service.Store.GetOperation(ctx, i.OriginalOperationID())
	if err != nil || original.State != ops.RecoveryRequired {
		return store.NativePreparedPointView{}, errEngineSource
	}
	backup, err := o.service.Store.ReadNativeBackupPolicy(ctx, input.BindingID)
	if err != nil {
		return store.NativePreparedPointView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(backup.Backup.MaxMaintenanceSeconds)*time.Second)
	defer cancel()
	work := nativePrepareWork{runtime: o.runtime, input: input, operation: original, stopped: stopped, permit: v.preparedPermit, startedAt: time.Now()}
	defer func() { err = errors.Join(err, work.close(ctx)) }()
	return work.capture(ctx)
}
