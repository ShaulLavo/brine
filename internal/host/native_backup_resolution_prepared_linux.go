//go:build linux

package host

import (
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/store"
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
	point, err := o.service.Store.CompleteNativePreparedPoint(ctx, stopped, v.extracted, v.observation)
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
