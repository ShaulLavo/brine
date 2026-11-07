//go:build linux

package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// CaptureBytes never opens live SQLite. The caller supplies admission and any
// required quiescence; a stable double read alone does not establish authority.
func CaptureBytes(ctx context.Context, stateDir string, ownerUID int) (c Capture, err error) {
	if err = ctx.Err(); err != nil {
		return c, err
	}
	dir, err := openControlDirectory(stateDir, ownerUID)
	if err != nil {
		return c, err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	first, err := readControlPair(ctx, dir, ownerUID)
	if err != nil {
		return c, err
	}
	second, err := readControlPair(ctx, dir, ownerUID)
	if err != nil {
		return c, err
	}
	if !sameControlCapture(first, second) {
		return c, ErrPreviewUnavailable
	}
	current, err := openControlDirectory(stateDir, ownerUID)
	if err != nil {
		return c, err
	}
	defer func() { err = errors.Join(err, current.Close()) }()
	originalInfo, err := dir.Stat()
	if err != nil {
		return c, err
	}
	currentInfo, err := current.Stat()
	if err != nil {
		return c, err
	}
	if !os.SameFile(originalInfo, currentInfo) {
		return c, ErrPreviewUnavailable
	}
	return first, ctx.Err()
}

func openControlDirectory(path string, ownerUID int) (_ *os.File, err error) {
	if ownerUID < 0 || uint64(ownerUID) > uint64(^uint32(0)) {
		return nil, ErrInvalid
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	root, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, unix.Close(root)) }()
	relative := strings.TrimPrefix(path, "/")
	if relative == "" {
		relative = "."
	}
	fd, err := unix.Openat2(root, relative, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	err = unix.Fstat(fd, &st)
	if err == nil && (int64(st.Uid) != int64(ownerUID) || st.Mode&0077 != 0) {
		err = ErrInvalid
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func readControlPair(ctx context.Context, dir *os.File, ownerUID int) (c Capture, err error) {
	c.DB, c.DBID, err = readControlFile(ctx, dir, "control.db", ownerUID, maxPreviewBytes)
	if errors.Is(err, os.ErrNotExist) {
		c.Absent = true
		err = nil
	}
	if err != nil {
		return Capture{}, err
	}
	c.WAL, c.WALID, err = readControlFile(ctx, dir, "control.db-wal", ownerUID, int64(maxPreviewBytes-len(c.DB)))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return Capture{}, err
	}
	if c.Absent && c.WAL != nil {
		return Capture{}, ErrPreviewUnavailable
	}
	return c, nil
}

func sameControlCapture(a, b Capture) bool {
	return a.Absent == b.Absent && a.DBID == b.DBID && a.WALID == b.WALID && bytes.Equal(a.DB, b.DB) && bytes.Equal(a.WAL, b.WAL)
}

func readControlFile(ctx context.Context, dir *os.File, name string, ownerUID int, limit int64) (_ []byte, id FileID, err error) {
	if err = ctx.Err(); err != nil {
		return nil, id, err
	}
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOATIME, 0)
	if err != nil {
		return nil, id, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { err = errors.Join(err, f.Close()) }()
	var before, after, named unix.Stat_t
	if err = unix.Fstat(fd, &before); err != nil {
		return nil, id, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0077 != 0 || int64(before.Uid) != int64(ownerUID) || before.Size < 0 || before.Size > limit {
		return nil, id, ErrPreviewUnavailable
	}
	data, err := readControlBytes(ctx, f, limit)
	if err != nil {
		return nil, id, err
	}
	if err = unix.Fstat(fd, &after); err != nil {
		return nil, id, err
	}
	if err = unix.Fstatat(int(dir.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, id, err
	}
	if !sameControlFile(before, after) || !sameControlFile(before, named) || int64(len(data)) != before.Size {
		return nil, id, ErrPreviewUnavailable
	}
	return data, FileID{Dev: uint64(before.Dev), Ino: before.Ino, Size: before.Size, SHA256: controlDigest(data)}, ctx.Err()
}

func sameControlFile(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Size == b.Size && a.Mode == b.Mode && a.Uid == b.Uid && a.Gid == b.Gid && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

func previewReadOwned(ctx context.Context, path string, ownerUID int) (_ []byte, err error) {
	dir, err := openControlDirectory(filepath.Dir(path), ownerUID)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	data, _, err := readControlFile(ctx, dir, filepath.Base(path), ownerUID, maxPreviewBytes)
	return data, err
}
