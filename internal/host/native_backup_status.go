package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativeBackupStatusReader struct{ stateDir string }

func (r nativeBackupStatusReader) BackupStatus(ctx context.Context, args dispatch.NativeBackupStatusArgs) (nativecapture.BackupStatus, error) {
	state, err := store.OpenReadOnly(ctx, r.stateDir)
	if err != nil {
		return nativecapture.BackupStatus{}, err
	}
	defer func() { _ = state.Close() }()
	return state.NativeBackupStatus(ctx, args.App, args.Database)
}
