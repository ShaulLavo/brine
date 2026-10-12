//go:build linux

package host

import (
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func engineOpenMember(root *os.Root, name string, flags uint64, mode uint32) (*os.File, error) {
	base, err := root.Open(".")
	if err != nil {
		return nil, errEngineSource
	}
	defer func() { _ = base.Close() }()
	how := unix.OpenHow{Flags: flags | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK, Mode: uint64(mode), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV}
	fd, err := unix.Openat2(int(base.Fd()), name, &how)
	if err != nil {
		return nil, errors.Join(errEngineSource, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

func engineOpenRoot(path string) (*os.Root, error) {
	if !engineMountPath(path) {
		return nil, errEngineSource
	}
	how := unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS}
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &how)
	if err != nil {
		return nil, errEngineSource
	}
	defer func() { _ = unix.Close(fd) }()
	return os.OpenRoot("/proc/self/fd/" + strconv.Itoa(fd))
}
