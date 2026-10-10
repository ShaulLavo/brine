package data

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
)

// StorageUsage is observed logical file bytes, not a hard rootless quota and
// not the filesystem's allocated blocks. Unknown traversal never reports zero.
type StorageUsage struct {
	Bytes uint64 `json:"bytes"`
	Files uint64 `json:"files"`
}

func ObserveUsage(ctx context.Context, b DatabaseBinding) (StorageUsage, error) {
	var usage StorageUsage
	relative, err := RelativeDirectory(b.IncarnationID, b.DatabaseID)
	if err != nil || relative != b.RelativeDirectory {
		return usage, ErrInvalid
	}
	source := filepath.Join(string(b.Root), relative)
	if err = verifyPrivateTree(string(b.Root), source); err != nil {
		return usage, err
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return usage, err
	}
	defer root.Close()
	visited := 0
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if depth > 16 {
			return ErrInvalid
		}
		if err := verifyPrivateTree(string(b.Root), filepath.Join(source, path)); err != nil {
			return err
		}
		directory, err := root.Open(path)
		if err != nil {
			return err
		}
		entries, readErr := directory.ReadDir(4097 - visited)
		directory.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		visited += len(entries)
		if visited > 4096 {
			return ErrInvalid
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			child := filepath.Join(path, entry.Name())
			info, err := root.Lstat(child)
			if err != nil {
				return err
			}
			if info.IsDir() {
				if err = walk(child, depth+1); err != nil {
					return err
				}
				continue
			}
			verified, err := verifyPrivateFile(filepath.Join(source, child))
			if err != nil || !os.SameFile(info, verified) || verified.Size() < 0 {
				return ErrInvalid
			}
			size := uint64(verified.Size())
			if size > math.MaxUint64-usage.Bytes {
				return ErrInvalid
			}
			usage.Bytes += size
			usage.Files++
		}
		return nil
	}
	if err = walk(".", 0); err != nil {
		return StorageUsage{}, err
	}
	return usage, nil
}
