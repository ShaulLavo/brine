//go:build linux

package store

import (
	"github.com/ShaulLavo/brine/internal/data"
	"golang.org/x/sys/unix"
	"os"
)

func nativeUploadDirectoryIdentity(root, relative string, held *os.Root) (data.EngineDirectoryIdentity, error) {
	if held == nil {
		return data.EngineDirectoryIdentity{}, ErrConflict
	}
	base, err := unix.Openat2(unix.AT_FDCWD, root, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return data.EngineDirectoryIdentity{}, ErrConflict
	}
	defer func() { _ = unix.Close(base) }()
	physical, err := nativeRestoreOpenDirectory(base, relative)
	if err != nil {
		return data.EngineDirectoryIdentity{}, ErrConflict
	}
	defer func() { _ = unix.Close(physical) }()
	stat, err := nativeRestoreStatDirectory(physical)
	if err != nil {
		return data.EngineDirectoryIdentity{}, err
	}
	file, err := held.Open(".")
	if err != nil {
		return data.EngineDirectoryIdentity{}, ErrConflict
	}
	defer func() { _ = file.Close() }()
	actual, err := nativeRestoreStatDirectory(int(file.Fd()))
	if err != nil || nativeRestoreDirectoryID(stat) != nativeRestoreDirectoryID(actual) || stat.Mnt_id != actual.Mnt_id {
		return data.EngineDirectoryIdentity{}, ErrConflict
	}
	return nativeRestoreDirectoryID(stat), nil
}
