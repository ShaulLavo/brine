package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
)

// OpenSnapshotReadOnly reads a bounded, WAL-aware private copy without opening
// live SQLite files. The caller supplies its own mutation fence when needed;
// this function never acquires or creates a live lock.
func OpenSnapshotReadOnly(ctx context.Context, stateDir string) (_ *Store, err error) {
	dir, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "brine-preview-")
	if err != nil {
		return nil, err
	}
	cleanup := func() error { return os.RemoveAll(tmp) }
	defer func() {
		if err != nil {
			err = errors.Join(err, cleanup())
		}
	}()
	if err := readOnlyDirectory(dir); err != nil {
		return nil, err
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
	read.cleanup = cleanup
	return read, nil
}

func readOnlyDirectory(dir string) error {
	if err := readOnlyOwner(dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return ErrInvalid
	}
	return nil
}

func previewRead(ctx context.Context, path string) ([]byte, error) {
	return previewReadOwned(ctx, path, os.Geteuid())
}
