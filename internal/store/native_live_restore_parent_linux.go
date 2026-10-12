//go:build linux

package store

import (
	"bytes"
	"context"
	"io"
	"os"
	"path"

	"github.com/ShaulLavo/brine/internal/data"
	"golang.org/x/sys/unix"
)

func checkNativeRestoreAnchors(ctx context.Context, capability *NativeLiveRestoreExecution, download *os.Root, observed NativeRestoreObservation) error {
	if ctx.Err() != nil || capability == nil || observed.Parent == nil || download == nil {
		return ErrConflict
	}
	binding := capability.Binding()
	base, err := unix.Openat2(unix.AT_FDCWD, string(binding.Root), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = unix.Close(base) }()
	root, err := nativeRestoreStatDirectory(base)
	if err != nil || nativeRestoreDirectoryID(root) != observed.TargetTree.Root {
		return ErrConflict
	}
	parent, err := nativeRestoreOpenDirectory(base, path.Join("transitions", capability.Attempt().TransitionID))
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = unix.Close(parent) }()
	parentStat, err := nativeRestoreStatDirectory(parent)
	if err != nil || parentStat.Mnt_id != root.Mnt_id || nativeRestoreDirectoryID(parentStat) != observed.TargetTree.Parent {
		return ErrConflict
	}
	heldParent, err := observed.Parent.Open(".")
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = heldParent.Close() }()
	heldStat, err := nativeRestoreStatDirectory(int(heldParent.Fd()))
	if err != nil || heldStat.Mnt_id != parentStat.Mnt_id || nativeRestoreDirectoryID(heldStat) != nativeRestoreDirectoryID(parentStat) {
		return ErrConflict
	}
	if err = nativeRestoreOwnerMarker(parent, capability.IntentSHA256()); err != nil {
		return err
	}
	return checkNativeRestoreDownloadAnchor(parent, root, download, observed.TargetTree.Directory)
}

func checkNativeRestoreDownloadAnchor(parent int, root unix.Statx_t, download *os.Root, expected data.EngineDirectoryIdentity) error {
	fd, err := nativeRestoreOpenDirectory(parent, "download")
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = unix.Close(fd) }()
	physical, err := nativeRestoreStatDirectory(fd)
	if err != nil || physical.Mnt_id != root.Mnt_id || nativeRestoreDirectoryID(physical) != expected {
		return ErrConflict
	}
	held, err := download.Open(".")
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = held.Close() }()
	heldStat, err := nativeRestoreStatDirectory(int(held.Fd()))
	if err != nil || heldStat.Mnt_id != physical.Mnt_id || nativeRestoreDirectoryID(heldStat) != expected {
		return ErrConflict
	}
	return nil
}

func nativeRestoreOpenDirectory(parent int, name string) (int, error) {
	return unix.Openat2(parent, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV})
}

func nativeRestoreStatDirectory(fd int) (unix.Statx_t, error) {
	var stat unix.Statx_t
	if unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &stat) != nil || stat.Mnt_id == 0 || stat.Mask&unix.STATX_MNT_ID == 0 || int64(stat.Uid) != int64(os.Geteuid()) || stat.Mode != unix.S_IFDIR|0700 {
		return stat, ErrConflict
	}
	return stat, nil
}

func nativeRestoreDirectoryID(stat unix.Statx_t) data.EngineDirectoryIdentity {
	return data.EngineDirectoryIdentity{Device: unix.Mkdev(stat.Dev_major, stat.Dev_minor), Inode: stat.Ino}
}

func nativeRestoreOwnerMarker(parent int, expected string) error {
	fd, err := unix.Openat2(parent, ".brine-restore-owner", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return ErrConflict
	}
	file := os.NewFile(uintptr(fd), ".brine-restore-owner")
	defer func() { _ = file.Close() }()
	var stat unix.Statx_t
	if unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS, &stat) != nil || stat.Mode != unix.S_IFREG|0600 || int64(stat.Uid) != int64(os.Geteuid()) || stat.Nlink != 1 || stat.Size != uint64(len(expected)) {
		return ErrConflict
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))
	if err != nil || !bytes.Equal(raw, []byte(expected)) {
		return ErrConflict
	}
	return nil
}
