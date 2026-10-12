//go:build linux

package workspaceobserve

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type fileIdentity struct{ device, inode uint64 }

func workspaceMembers(ctx context.Context, directory *os.File, uid uint32, held directoryIdentity) (map[fileIdentity]bool, error) {
	members := map[fileIdentity]bool{}
	pending := []string{"."}
	for len(pending) > 0 {
		if ctx.Err() != nil || len(members) > enumerationLimit {
			return nil, ErrUnknown
		}
		relative := pending[0]
		pending = pending[1:]
		file, member, err := privateMember(directory, relative, uid, held)
		if err != nil {
			return nil, ErrUnknown
		}
		key := fileIdentity{member.Device, member.Inode}
		if members[key] {
			_ = file.Close()
			return nil, ErrUnknown
		}
		members[key] = true
		entries, err := file.ReadDir(enumerationLimit + 1)
		closeErr := file.Close()
		if err != nil && !errors.Is(err, io.EOF) || closeErr != nil || len(entries) > enumerationLimit {
			return nil, ErrUnknown
		}
		for _, entry := range entries {
			next := filepath.Join(relative, entry.Name())
			file, member, err := privateMember(directory, next, uid, held)
			if err != nil {
				return nil, ErrUnknown
			}
			info, err := file.Stat()
			_ = file.Close()
			if err != nil {
				return nil, ErrUnknown
			}
			if info.IsDir() {
				pending = append(pending, next)
				continue
			}
			key := fileIdentity{member.Device, member.Inode}
			if members[key] {
				return nil, ErrUnknown
			}
			members[key] = true
		}
	}
	return members, nil
}
func privateMember(directory *os.File, relative string, uid uint32, held directoryIdentity) (*os.File, directoryIdentity, error) {
	how := unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV}
	fd, err := unix.Openat2(int(directory.Fd()), relative, &how)
	if err != nil {
		return nil, directoryIdentity{}, ErrUnknown
	}
	file := os.NewFile(uintptr(fd), relative)
	var stat unix.Statx_t
	err = unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &stat)
	valid := stat.Mode == unix.S_IFDIR|0700 || stat.Mode == unix.S_IFREG|0600 && stat.Nlink == 1
	member := directoryIdentity{unix.Mkdev(stat.Dev_major, stat.Dev_minor), stat.Ino, stat.Mnt_id}
	if err != nil || stat.Uid != uid || !valid || stat.Mask&unix.STATX_MNT_ID == 0 || member.Mount != held.Mount || member.Device != held.Device {
		_ = file.Close()
		return nil, directoryIdentity{}, ErrUnknown
	}
	return file, member, nil
}
