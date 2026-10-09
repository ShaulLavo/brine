//go:build linux

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Lock is independent of database transactions. Its file must never be unlinked.
// Linux releases flock on process death, including SIGKILL, without PID/boot-ID
// heuristics or a stale-lease window. Separate opens also exclude one process's
// concurrent goroutines. Releasing a Store does not release an outstanding lock.
type Lock interface{ Release() error }
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
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func (s *Store) AcquireHostLock(ctx context.Context) (Lock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := openPrivateFile(filepath.Join(s.dir, "mutation.lock"))
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
