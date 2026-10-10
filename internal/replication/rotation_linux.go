//go:build linux

package replication

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ReplaceService is an explicit expected-old-to-new update under the caller's
// host and replica lifetime locks. Config and lifetime-lock inodes are untouched.
// An already-installed exact new unit reconciles an interrupted replacement.
func (p ArtifactPublisher) ReplaceService(ctx context.Context, a Artifacts, beforeHash string) (resultErr error) {
	name, err := ServiceName(a.Binding.BindingID)
	if err != nil || ctx.Err() != nil || a.ServicePath != filepath.Join(p.UnitRoot, name) || len(a.Service) == 0 || len(a.Service) > MaxConfigBytes || !safePath(p.UnitRoot) || len(beforeHash) != 64 {
		return ErrPublish
	}
	if _, err := hex.DecodeString(beforeHash); err != nil {
		return ErrPublish
	}
	dir, err := openDirectory(p.UnitRoot)
	if err != nil {
		return ErrPublish
	}
	defer func() {
		if unix.Close(dir) != nil {
			resultErr = ErrPublish
		}
	}()
	if !privateDirectory(dir) {
		return ErrPublish
	}
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return ErrPublish
	}
	file := os.NewFile(uintptr(fd), name)
	var stat unix.Stat_t
	valid := unix.Fstat(fd, &stat) == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Mode&07777 == 0600 && int64(stat.Uid) == int64(os.Geteuid()) && stat.Nlink == 1 && stat.Size > 0 && stat.Size <= MaxConfigBytes
	raw, readErr := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	closeErr := file.Close()
	if !valid || readErr != nil || closeErr != nil {
		return ErrPublish
	}
	if bytes.Equal(raw, a.Service) {
		return p.fsync(ctx, dir)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != beforeHash {
		return ErrPublish
	}
	temporary := ".brine-rotation-" + rand.Text() + ".tmp"
	nextFD, err := unix.Openat(dir, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return ErrPublish
	}
	defer func() {
		if err := unix.Unlinkat(dir, temporary, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			resultErr = ErrPublish
		}
	}()
	next := os.NewFile(uintptr(nextFD), temporary)
	_, err = next.Write(a.Service)
	if err == nil {
		err = p.fsync(ctx, nextFD)
	}
	closeErr = next.Close()
	if err != nil || closeErr != nil || ctx.Err() != nil {
		return ErrPublish
	}
	var current unix.Stat_t
	if unix.Fstatat(dir, name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || current.Dev != stat.Dev || current.Ino != stat.Ino || current.Mtim != stat.Mtim || current.Size != stat.Size {
		return ErrPublish
	}
	if err := unix.Renameat(dir, temporary, dir, name); err != nil {
		return ErrPublish
	}
	exists, err := p.checkFile(ctx, dir, name, a.Service)
	if err != nil || !exists {
		return ErrPublish
	}
	return p.fsync(ctx, dir)
}
