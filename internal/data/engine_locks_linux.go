//go:build linux

package data

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var ErrEngineLocked = errors.New("engine lock is held")

// PrepareEngineLocks belongs to reviewed preparation, never startup.
func PrepareEngineLocks(ctx context.Context, stateRoot string, binding ReplicaBindingID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidID(string(binding)) {
		return ErrInvalid
	}
	root, err := openEngineDirectory(stateRoot)
	if err != nil {
		return err
	}
	defer unix.Close(root)
	if err = unix.Mkdirat(root, "engine-locks", 0700); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	parent, err := openEngineDirectory(filepath.Join(stateRoot, "engine-locks"))
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	for _, suffix := range []string{".launch", ".lifetime"} {
		file, err := openEngineLock(parent, string(binding)+suffix, true)
		if err != nil {
			return err
		}
		if err = file.Close(); err != nil {
			return err
		}
	}
	return unix.Fsync(parent)
}

func AcquireEngineLaunchGuard(ctx context.Context, stateRoot string, binding ReplicaBindingID) (*EngineLock, error) {
	return acquireEngineLock(ctx, stateRoot, binding, ".launch", true)
}

func AcquireEngineLifetimeLock(ctx context.Context, stateRoot string, binding ReplicaBindingID) (*EngineLock, error) {
	return acquireEngineLock(ctx, stateRoot, binding, ".lifetime", false)
}

func acquireEngineLock(ctx context.Context, root string, binding ReplicaBindingID, suffix string, wait bool) (*EngineLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ValidID(string(binding)) {
		return nil, ErrInvalid
	}
	parent, err := openEngineDirectory(filepath.Join(root, "engine-locks"))
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	file, err := openEngineLock(parent, string(binding)+suffix, false)
	if err != nil {
		return nil, err
	}
	if err = takeEngineLock(ctx, file, wait); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return &EngineLock{file: file}, nil
}

func takeEngineLock(ctx context.Context, file *os.File, wait bool) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		if !wait {
			return ErrEngineLocked
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func openEngineLock(parent int, name string, create bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if create {
		flags = unix.O_RDWR | unix.O_CREAT | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	}
	fd, err := unix.Openat(parent, name, flags, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0777 != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return nil, errors.Join(ErrInvalid, file.Close())
	}
	return file, nil
}

func openEngineDirectory(path string) (int, error) {
	if !ValidRoot(path) {
		return -1, ErrInvalid
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&0777 != 0700 || stat.Uid != uint32(os.Geteuid()) {
		unix.Close(fd)
		return -1, ErrInvalid
	}
	return fd, nil
}
