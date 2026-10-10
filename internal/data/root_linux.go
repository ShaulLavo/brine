//go:build linux

package data

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// InspectRoot is read-only. ProbeRoot supplies affirmative locking/rename
// evidence separately; inventory inspection alone cannot invent those proofs.
func InspectRoot(root string) (RootEvidence, error) {
	var e RootEvidence
	e.Root = PersistentRoot(root)
	e.ObservedAt = time.Now().UTC()
	if !ValidRoot(root) {
		return e, ErrInvalid
	}
	if err := verifyRootAncestors(root); err != nil {
		return e, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return e, err
	}
	if !info.IsDir() || !owned(info) || info.Mode().Perm() != 0700 {
		return e, ErrInvalid
	}
	var stat unix.Stat_t
	if err = unix.Lstat(root, &stat); err != nil {
		return e, err
	}
	e.Device = uint64(stat.Dev)
	e.Inode = stat.Ino
	var fs unix.Statfs_t
	if err = unix.Statfs(root, &fs); err != nil {
		return e, err
	}
	switch fs.Type {
	case unix.EXT4_SUPER_MAGIC:
		e.Filesystem = "ext4"
	case unix.XFS_SUPER_MAGIC:
		e.Filesystem = "xfs"
	case unix.BTRFS_SUPER_MAGIC:
		e.Filesystem = "btrfs"
	default:
		return e, ErrInvalid
	}
	if fs.Bsize <= 0 || fs.Bavail > math.MaxUint64/uint64(fs.Bsize) {
		return e, ErrInvalid
	}
	e.FreeBytes = fs.Bavail * uint64(fs.Bsize)
	e.FreeInodes = fs.Ffree
	return e, nil
}

// ProbeRoot performs bounded local file effects only at the authorized root.
// OFD and POSIX byte-range locks conflict even within one process, unlike two
// POSIX descriptors owned by the same process, which cannot prove exclusion.
func ProbeRoot(ctx context.Context, root string) (e RootEvidence, err error) {
	if err = ctx.Err(); err != nil {
		return e, err
	}
	e, err = InspectRoot(root)
	if err != nil {
		return e, err
	}
	id, err := NewID()
	if err != nil {
		return e, err
	}
	first := filepath.Join(root, ".brine-probe-"+id)
	second := first + "-renamed"
	fd, err := unix.Open(first, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return e, err
	}
	f := os.NewFile(uintptr(fd), first)
	defer func() {
		err = errors.Join(err, f.Close())
		if removeErr := os.Remove(first); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, removeErr)
		}
		if removeErr := os.Remove(second); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, removeErr)
		}
	}()
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 1}
	if err = unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lock); err != nil {
		return e, err
	}
	otherFD, err := unix.Open(first, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return e, err
	}
	conflict := unix.FcntlFlock(uintptr(otherFD), unix.F_OFD_SETLK, &lock)
	closeErr := unix.Close(otherFD)
	if closeErr != nil {
		return e, closeErr
	}
	if !errors.Is(conflict, unix.EAGAIN) && !errors.Is(conflict, unix.EACCES) {
		return e, ErrInvalid
	}
	e.POSIXLocks = true
	if err = ctx.Err(); err != nil {
		return e, err
	}
	if _, err = f.Write([]byte("brine-fsync-probe-v1")); err != nil {
		return e, err
	}
	if err = f.Sync(); err != nil {
		return e, err
	}
	if err = os.Rename(first, second); err != nil {
		return e, err
	}
	parent, err := os.Open(root)
	if err != nil {
		return e, err
	}
	err = parent.Sync()
	closeErr = parent.Close()
	if err != nil {
		return e, err
	}
	if closeErr != nil {
		return e, closeErr
	}
	e.DurableRename = true
	return e, nil
}
