//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativeBackupOperations struct {
	service Service
	jobs    jobs.Service
}

func (o nativeBackupOperations) Capture(ctx context.Context, args dispatch.NativeCaptureArgs) (jobs.Accepted, error) {
	if !args.Valid() {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	return o.submit(ctx, args.App, args.Database, args.IdempotencyKey, ops.NativeCapture)
}
func (o nativeBackupOperations) Prepare(ctx context.Context, args dispatch.NativePrepareArgs) (jobs.Accepted, error) {
	if !args.Valid() {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	return o.submit(ctx, args.App, args.Database, args.IdempotencyKey, ops.NativePrepare)
}
func (o nativeBackupOperations) submit(ctx context.Context, app, database, key string, kind ops.Kind) (jobs.Accepted, error) {
	binding, err := o.service.Store.SelectNativeTaskBinding(ctx, app, database)
	if err != nil {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	return o.submitBinding(ctx, binding, app, key, kind, "")
}
func (o nativeBackupOperations) submitBinding(ctx context.Context, binding data.ReplicaBindingID, app, key string, kind ops.Kind, pointID string) (jobs.Accepted, error) {
	backup, err := o.service.Store.ReadNativeBackupPolicy(ctx, binding)
	pol, policyErr := o.service.Policy.Load(ctx)
	if err != nil || policyErr != nil || pol.Hash() != "sha256:"+backup.PolicySHA256 {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	requester, err := datainit.ParseRequester(o.jobs.Requester)
	if err != nil {
		return jobs.Accepted{}, err
	}
	reference, err := o.service.Store.SaveNativeTaskInput(ctx, store.NativeTaskInput{Requester: requester, Kind: kind, BindingID: binding, PointID: pointID})
	if err != nil {
		return jobs.Accepted{}, err
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: kind, App: app, SecretRef: reference}, key)
}
func (o nativeBackupOperations) TestRestore(ctx context.Context, args dispatch.NativeRestoreTestArgs) (jobs.Accepted, error) {
	if !args.Valid() {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	point, err := o.service.Store.ReadNativeVerifiedPoint(ctx, args.PointID)
	if err != nil {
		return jobs.Accepted{}, err
	}
	app, err := o.service.Store.ActiveNativeBindingApp(ctx, point.BindingID)
	if err != nil {
		return jobs.Accepted{}, err
	}
	return o.submitBinding(ctx, point.BindingID, app, args.IdempotencyKey, ops.NativeRestoreTest, args.PointID)
}
