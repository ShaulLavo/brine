package store

import (
	"bytes"
	"context"
	"errors"
	"io"
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
func OpenPreviewReadOnly(ctx context.Context, stateDir string) (_ *Store, err error) {
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
	tmp, err := os.MkdirTemp("", "brine-preview-")
	cleanup := func() error {
		var errs []error
		if host != nil {
			errs = append(errs, host.Release())
		}
		if launch != nil {
			errs = append(errs, launch.Release())
		}
		if tmp != "" {
			errs = append(errs, os.RemoveAll(tmp))
		}
		return errors.Join(errs...)
	}
	if err != nil {
		cleanup()
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, cleanup())
		}
	}()
	if e := readOnlyOwner(dir); e != nil {
		return nil, e
	}
	info, e := os.Lstat(dir)
	if e != nil {
		return nil, e
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	captures := map[string][]byte{}
	total := int64(0)
	for _, name := range []string{"control.db", "control.db-wal"} {
		data, e := previewRead(ctx, filepath.Join(dir, name))
		if errors.Is(e, os.ErrNotExist) && name != "control.db" {
			continue
		}
		if e != nil {
			return nil, e
		}
		total += int64(len(data))
		if total > maxPreviewBytes {
			return nil, ErrPreviewUnavailable
		}
		captures[name] = data
		if e = os.WriteFile(filepath.Join(tmp, name), data, 0600); e != nil {
			return nil, e
		}
	}
	// A plan writer need not hold the host lock. Refuse a torn file capture rather
	// than claiming a coherent preview or retrying against an unknown generation.
	for _, name := range []string{"control.db", "control.db-wal"} {
		data, e := previewRead(ctx, filepath.Join(dir, name))
		if errors.Is(e, os.ErrNotExist) && name != "control.db" {
			data = nil
			e = nil
		}
		if e != nil {
			return nil, e
		}
		if !bytes.Equal(data, captures[name]) {
			return nil, ErrPreviewUnavailable
		}
	}
	read, err := OpenReadOnly(ctx, tmp)
	if err != nil {
		return nil, err
	}
	read.dir = dir
	read.previewHost, read.previewLaunch = host, launch
	read.cleanup = cleanup
	return read, nil
}
func previewRead(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := readOnlyOwner(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxPreviewBytes {
		return nil, ErrPreviewUnavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, ErrPreviewUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPreviewBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPreviewBytes {
		return nil, ErrPreviewUnavailable
	}
	return data, ctx.Err()
}
