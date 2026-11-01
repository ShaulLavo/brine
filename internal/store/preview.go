package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
)

var ErrPreviewUnavailable = errors.New("control preview snapshot unavailable")

const maxPreviewBytes = 64 << 20

type previewLock struct{}

func (previewLock) Release() error { return nil }

// OpenPreviewReadOnly fences existing host/launch locks and reads an isolated
// copy. SQLite can rebuild the copy's WAL index without changing the enrolled
// database's shared memory, bytes, permissions, or timestamps. No lock is created.
// Missing fences permit only schema inspection, never an operation assessment.
func OpenPreviewReadOnly(ctx context.Context, stateDir string) (*Store, error) {
	dir, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	source := &Store{dir: dir, readOnly: true}
	wait, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	var launch, host ops.Lock
	launch, err = source.AcquireLaunchLock(wait)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		host, err = source.AcquireHostLock(wait)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			launch.Release()
			return nil, err
		}
	}
	cleanup := func() error {
		var errs []error
		if host != nil {
			errs = append(errs, host.Release())
		}
		if launch != nil {
			errs = append(errs, launch.Release())
		}
		return errors.Join(errs...)
	}
	read, err := OpenSnapshotReadOnly(ctx, dir)
	if err != nil {
		return nil, errors.Join(err, cleanup())
	}
	snapshotCleanup := read.cleanup
	read.previewHost, read.previewLaunch = host, launch
	read.cleanup = func() error { return errors.Join(cleanup(), snapshotCleanup()) }
	return read, nil
}
