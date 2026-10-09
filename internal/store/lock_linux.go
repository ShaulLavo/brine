//go:build linux

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"golang.org/x/sys/unix"
)

// Lock is independent of database transactions. Its file must never be unlinked.
// Linux releases flock on process death, including SIGKILL, without PID/boot-ID
// heuristics or a stale-lease window. Separate opens also exclude one process's
// concurrent goroutines. Releasing a Store does not release an outstanding lock.
type Lock = ops.Lock
type hostLock struct {
	file *os.File
	once sync.Once
	err  error
}

func (l *hostLock) Release() error { l.once.Do(func() { l.err = l.file.Close() }); return l.err }
func openPrivateFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalid
	}
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil {
		f.Close()
		return nil, err
	}
	if stat.Uid != uint32(os.Geteuid()) {
		f.Close()
		return nil, ErrInvalid
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func (s *Store) AcquireHostLock(ctx context.Context) (Lock, error) {
	if s.previewHost != nil {
		return previewLock{}, nil
	}
	return s.acquireLock(ctx, filepath.Join(s.dir, "mutation.lock"))
}

// AcquireLaunchLock fences operation creation and launch settlement independently
// of long-running deployments. When both are needed, take launch before host.
func (s *Store) AcquireLaunchLock(ctx context.Context) (Lock, error) {
	if s.previewLaunch != nil {
		return previewLock{}, nil
	}
	return s.acquireLock(ctx, filepath.Join(s.dir, "launch.lock"))
}

func acquireLock(ctx context.Context, path string) (Lock, error) {
	return (&Store{}).acquireLock(ctx, path)
}
func (s *Store) acquireLock(ctx context.Context, path string) (Lock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var f *os.File
	var err error
	if s.readOnly {
		// Preview may fence existing files, but never create/chmod a lock file.
		fd, e := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		err = e
		if e == nil {
			f = os.NewFile(uintptr(fd), path)
			var st unix.Stat_t
			err = unix.Fstat(fd, &st)
			if err == nil && (st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0) {
				err = ErrInvalid
			}
			if err != nil {
				f.Close()
			}
		}
	} else {
		f, err = openPrivateFile(path)
	}
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err = ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &hostLock{file: f}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func secureStateDir(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return ErrInvalid
	}
	return unix.Fchmod(fd, 0700)
}

func readOnlyOwner(path string) error {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return err
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return ErrInvalid
	}
	return nil
}
