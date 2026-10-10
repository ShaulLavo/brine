//go:build linux

package replication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

var ErrLocked = errors.New("replication: lifetime lock unavailable")

type LifetimeLock struct {
	file *os.File
	once sync.Once
	err  error
}

func (l *LifetimeLock) Release() error { l.once.Do(func() { l.err = l.file.Close() }); return l.err }

func (l *LifetimeLock) inherit() error {
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(l.file.Fd(), unix.F_GETFD, 0)
	if err != nil {
		return err
	}
	_, err = unix.FcntlInt(l.file.Fd(), unix.F_SETFD, flags & ^unix.FD_CLOEXEC)
	return err
}

// AcquireLifetimeLock never waits, repairs permissions or unlinks a lock file.
// The parent must already be a private runner-owned directory. openat walks
// every ancestor without following symlinks before creating the fixed inode.
func AcquireLifetimeLock(ctx context.Context, file string) (*LifetimeLock, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !safePath(file) {
		return nil, ErrInvalid
	}
	dir, err := openDirectory(filepath.Dir(file))
	if err != nil {
		return nil, ErrInvalid
	}
	defer unix.Close(dir)
	var ds unix.Stat_t
	if unix.Fstat(dir, &ds) != nil || ds.Uid != uint32(os.Geteuid()) || ds.Mode&0777 != 0700 {
		return nil, ErrInvalid
	}
	fd, err := unix.Openat(dir, filepath.Base(file), unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, ErrInvalid
	}
	f := os.NewFile(uintptr(fd), file)
	var fs unix.Stat_t
	if unix.Fstat(fd, &fs) != nil || fs.Mode&unix.S_IFMT != unix.S_IFREG || fs.Uid != uint32(os.Geteuid()) || fs.Mode&0777 != 0600 || fs.Nlink != 1 {
		f.Close()
		return nil, ErrInvalid
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrLocked
	}
	if ctx.Err() != nil {
		f.Close()
		return nil, ctx.Err()
	}
	return &LifetimeLock{file: f}, nil
}
func openDirectory(dir string) (int, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(dir, "/"), "/") {
		if part == "" {
			continue
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}
