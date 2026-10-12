//go:build linux

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"golang.org/x/sys/unix"
)

func checkPublicationCheckpointMembers(ctx context.Context, root *os.Root, checkpoint nativecapture.Checkpoint) error {
	if ctx.Err() != nil || root == nil || checkpoint.SHA256() == "" {
		return ErrConflict
	}
	base, err := root.Open(".")
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = base.Close() }()
	identity, err := nativeRestoreStatDirectory(int(base.Fd()))
	if err != nil {
		return err
	}
	for _, member := range checkpoint.Record().Members {
		if err = checkPublicationCheckpointMember(ctx, int(base.Fd()), identity.Mnt_id, member); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func checkPublicationCheckpointMember(ctx context.Context, base int, mount uint64, member nativecapture.CheckpointMember) error {
	fd, err := unix.Openat2(base, member.Path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return ErrConflict
	}
	file := os.NewFile(uintptr(fd), member.Path)
	defer func() { _ = file.Close() }()
	var before, after unix.Statx_t
	if unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || int64(before.Uid) != int64(os.Geteuid()) || before.Size != member.Bytes || before.Mnt_id != mount {
		return ErrConflict
	}
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(file, int64(member.Bytes)+1))
	if err != nil || ctx.Err() != nil || count < 0 || uint64(count) != member.Bytes || hex.EncodeToString(hash.Sum(nil)) != member.SHA256 || unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &after) != nil || !publicationCheckpointStatUnchanged(before, after) {
		return ErrConflict
	}
	return nil
}

func publicationCheckpointStatUnchanged(before, after unix.Statx_t) bool {
	return before.Mask == after.Mask && before.Ino == after.Ino && before.Dev_major == after.Dev_major && before.Dev_minor == after.Dev_minor && before.Mnt_id == after.Mnt_id && before.Mode == after.Mode && before.Uid == after.Uid && before.Gid == after.Gid && before.Nlink == after.Nlink && before.Size == after.Size && before.Mtime == after.Mtime && before.Ctime == after.Ctime
}
