//go:build linux

package replication

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ErrPublish = errors.New("replication: artifact publication refused or unknown")

// ArtifactPublisher requires existing private runner-owned state and user-unit
// roots. It publishes the service last and never starts or reloads anything.
// Config/unit drift is refused, not overwritten. Lifetime lock inodes are never
// replaced or unlinked, including during withdrawal of a replica.
type ArtifactPublisher struct {
	StateRoot, UnitRoot string
	sync                func(int) error
}

func (p ArtifactPublisher) Publish(ctx context.Context, a Artifacts) error {
	name, err := ServiceName(a.Binding.BindingID)
	if err != nil || ctx.Err() != nil || !safePath(p.StateRoot) || !safePath(p.UnitRoot) || a.ConfigPath != filepath.Join(p.StateRoot, "replication", a.Binding.BindingID, "litestream.yml") || a.Binding.SocketPath != filepath.Join(p.StateRoot, "replication", a.Binding.BindingID, "control.sock") || a.LifetimeLock != filepath.Join(p.StateRoot, "replica-locks", a.Binding.BindingID+".lock") || a.ServicePath != filepath.Join(p.UnitRoot, name) || len(a.Service) == 0 || len(a.Service) > MaxConfigBytes {
		return ErrPublish
	}
	if _, err = ParseConfig(a.Config, a.Binding); err != nil {
		return ErrPublish
	}
	state, err := openDirectory(p.StateRoot)
	if err != nil {
		return ErrPublish
	}
	defer unix.Close(state)
	units, err := openDirectory(p.UnitRoot)
	if err != nil {
		return ErrPublish
	}
	defer unix.Close(units)
	if !privateDirectory(state) || !privateDirectory(units) {
		return ErrPublish
	}
	config, err := p.directory(ctx, state, "replication", a.Binding.BindingID)
	if err != nil {
		return ErrPublish
	}
	defer unix.Close(config)
	locks, err := p.directory(ctx, state, "replica-locks")
	if err != nil {
		return ErrPublish
	}
	defer unix.Close(locks)
	for _, file := range []struct {
		dir  int
		name string
		raw  []byte
	}{{config, "litestream.yml", a.Config}, {locks, filepath.Base(a.LifetimeLock), nil}, {units, name, a.Service}} {
		if err = p.publishFile(ctx, file.dir, file.name, file.raw); err != nil {
			return ErrPublish
		}
	}
	return nil
}
func privateDirectory(fd int) bool {
	var s unix.Stat_t
	return unix.Fstat(fd, &s) == nil && s.Mode&unix.S_IFMT == unix.S_IFDIR && s.Uid == uint32(os.Geteuid()) && s.Mode&07777 == 0700
}
func (p ArtifactPublisher) fsync(ctx context.Context, fd int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sync := p.sync
	if sync == nil {
		sync = unix.Fsync
	}
	if err := sync(fd); err != nil {
		return err
	}
	return ctx.Err()
}
func (p ArtifactPublisher) directory(ctx context.Context, root int, parts ...string) (int, error) {
	current, err := unix.Dup(root)
	if err != nil {
		return -1, err
	}
	unix.CloseOnExec(current)
	for _, part := range parts {
		if err = ctx.Err(); err != nil {
			unix.Close(current)
			return -1, err
		}
		if err = unix.Mkdirat(current, part, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			unix.Close(current)
			return -1, err
		}
		next, e := unix.Openat(current, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil || !privateDirectory(next) {
			if e == nil {
				unix.Close(next)
			}
			unix.Close(current)
			return -1, ErrPublish
		}
		// Repeat parent/child fsync on exact retries: a prior mkdir may have succeeded
		// before its durability became unknown.
		e = p.fsync(ctx, current)
		unix.Close(current)
		if e != nil {
			unix.Close(next)
			return -1, e
		}
		current = next
	}
	if err = p.fsync(ctx, current); err != nil {
		unix.Close(current)
		return -1, err
	}
	return current, nil
}
func (p ArtifactPublisher) checkFile(ctx context.Context, dir int, name string, raw []byte) (bool, error) {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var s unix.Stat_t
	if unix.Fstat(fd, &s) != nil || s.Mode&unix.S_IFMT != unix.S_IFREG || s.Mode&07777 != 0600 || s.Uid != uint32(os.Geteuid()) || s.Nlink != 1 || s.Size != int64(len(raw)) {
		return false, ErrPublish
	}
	actual, err := io.ReadAll(io.LimitReader(f, int64(len(raw))+1))
	if err != nil || !bytes.Equal(actual, raw) {
		return false, ErrPublish
	}
	if err = p.fsync(ctx, fd); err != nil {
		return false, err
	}
	return true, nil
}
func (p ArtifactPublisher) publishFile(ctx context.Context, dir int, name string, raw []byte) error {
	exists, err := p.checkFile(ctx, dir, name, raw)
	if err != nil {
		return err
	}
	if exists {
		return p.fsync(ctx, dir)
	}
	tmp := ".brine-replica-" + rand.Text() + ".tmp"
	fd, err := unix.Openat(dir, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(dir, tmp, 0)
	f := os.NewFile(uintptr(fd), tmp)
	if _, err = f.Write(raw); err == nil {
		err = p.fsync(ctx, fd)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = unix.Renameat2(dir, tmp, dir, name, unix.RENAME_NOREPLACE); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	// A competing exact publication is safe, but never replace its lock inode.
	exists, err = p.checkFile(ctx, dir, name, raw)
	if err != nil || !exists {
		return ErrPublish
	}
	return p.fsync(ctx, dir)
}
