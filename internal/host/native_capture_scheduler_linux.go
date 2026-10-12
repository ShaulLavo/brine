//go:build linux

package host

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

func nativeCaptureScheduler(operations nativeBackupOperations, runtime nativeBackupRuntime) func(context.Context, string) (jobs.Accepted, error) {
	return func(ctx context.Context, binding string) (jobs.Accepted, error) {
		if !data.ValidID(binding) {
			return jobs.Accepted{}, errEngineSource
		}
		policy, err := runtime.state.ReadNativeBackupPolicy(ctx, data.ReplicaBindingID(binding))
		if err != nil {
			return jobs.Accepted{}, err
		}
		plan, err := runtime.state.ReadNativeBackupRevisionPlanForPolicy(ctx, policy)
		if err != nil {
			return jobs.Accepted{}, err
		}
		metadata := store.NativeBackupActivationMetadata{Plan: plan, Previous: policy}
		unit, _ := nativeBackupUnitBytes(metadata)
		root, err := engineOpenRoot(runtime.home)
		if err != nil {
			return jobs.Accepted{}, err
		}
		defer func() { _ = root.Close() }()
		path := filepath.Join(".config/systemd/user", nativeBackupUnitName(metadata)+".service")
		raw, err := readEnginePrivateFile(ctx, root, path, policy.UnitSHA256, 16<<10)
		if err != nil || nativeBackupBytesHash(raw) != nativeBackupBytesHash(unit) {
			return jobs.Accepted{}, errEngineSource
		}
		self, err := engineStarterProcess()
		if err != nil {
			return jobs.Accepted{}, err
		}
		parsed, err := systemd.ParseUnit(nativeBackupUnitName(metadata) + ".service")
		if err != nil {
			return jobs.Accepted{}, err
		}
		if _, err := systemd.New(runtime.session).StartupInvocation(ctx, parsed, self); err != nil {
			return jobs.Accepted{}, err
		}
		key, err := data.NewID()
		if err != nil {
			return jobs.Accepted{}, err
		}
		app, err := runtime.state.ActiveNativeBindingApp(ctx, policy.BindingID)
		if err != nil {
			return jobs.Accepted{}, err
		}
		if operations.jobs.Requester == "" {
			return jobs.Accepted{}, fmt.Errorf("native scheduler requester unavailable")
		}
		return operations.submitBinding(ctx, policy.BindingID, app, key, ops.NativeCapture, "")
	}
}
