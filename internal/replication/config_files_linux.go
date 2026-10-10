//go:build linux

package replication

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type DiskConfigs struct{}

// ReadConfig never creates files, follows symlinks or repairs permissions.
func (DiskConfigs) ReadConfig(ctx context.Context, path string) ([]byte, error) {
	if ctx.Err() != nil || !safePath(path) {
		return nil, ErrPermit
	}
	dir, err := openDirectory(filepath.Dir(path))
	if err != nil {
		return nil, ErrPermit
	}
	defer unix.Close(dir)
	var ds unix.Stat_t
	if unix.Fstat(dir, &ds) != nil || ds.Uid != uint32(os.Geteuid()) || ds.Mode&0777 != 0700 {
		return nil, ErrPermit
	}
	fd, err := unix.Openat(dir, filepath.Base(path), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrPermit
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0777 != 0600 || stat.Nlink != 1 || stat.Size <= 0 || stat.Size > MaxConfigBytes {
		return nil, ErrPermit
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	if err != nil || ctx.Err() != nil || len(raw) == 0 || len(raw) > MaxConfigBytes {
		return nil, ErrPermit
	}
	return raw, nil
}
