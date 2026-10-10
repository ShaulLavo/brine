//go:build linux || darwin

package backupcredentials

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Root is the existing private runner credentials directory, never an app mount.
type Files struct{ Root string }

func fileName(ref string, n uint64, suffix string) string {
	return fmt.Sprintf("s3/%s/v%d.%s", ref, n, suffix)
}
func protected(info os.FileInfo, dir bool, mode os.FileMode) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid() && info.Mode().Perm() == mode && ((dir && info.IsDir()) || (!dir && info.Mode().IsRegular() && st.Nlink == 1))
}
func safeAncestor(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && (int(st.Uid) == os.Geteuid() || st.Uid == 0) &&
		(info.Mode().Perm()&0022 == 0 || st.Uid == 0 && info.Mode()&os.ModeSticky != 0)
}

// Pin every component from the filesystem root: checking pathnames before
// os.OpenRoot is insufficient because OpenRoot follows directory symlinks.
func pinCredentialRoot(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, ErrStorage
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrStorage
	}
	current := os.NewFile(uintptr(fd), "/")
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		info, err := current.Stat()
		if err != nil || !safeAncestor(info) {
			current.Close()
			return nil, ErrStorage
		}
		next, err := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		current.Close()
		if err != nil {
			return nil, ErrStorage
		}
		current = os.NewFile(uintptr(next), component)
	}
	info, err := current.Stat()
	if err != nil || !protected(info, true, 0700) {
		current.Close()
		return nil, ErrStorage
	}
	return current, nil
}

func openMatchingCredentialRoot(path string, pinned os.FileInfo) (*os.Root, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrStorage
	}
	info, err := root.Stat(".")
	if err != nil || !os.SameFile(pinned, info) || !protected(info, true, 0700) {
		root.Close()
		return nil, ErrStorage
	}
	return root, nil
}

func (f Files) open() (*os.Root, error) {
	pinned, err := pinCredentialRoot(f.Root)
	if err != nil {
		return nil, ErrStorage
	}
	defer pinned.Close()
	info, err := pinned.Stat()
	if err != nil {
		return nil, ErrStorage
	}
	// Keep the pinned FD alive through the identity comparison; a pathname
	// substitution cannot make an unrelated directory inherit this inode.
	return openMatchingCredentialRoot(f.Root, info)
}
func ensure(root *os.Root, ref string) error {
	if !namePattern.MatchString(ref) {
		return ErrInvalid
	}
	for _, p := range []string{"s3", "s3/" + ref} {
		err := root.Mkdir(p, 0700)
		if err != nil && !errors.Is(err, os.ErrExist) {
			return ErrStorage
		}
		info, err := root.Lstat(p)
		if err != nil || !protected(info, true, 0700) {
			return ErrStorage
		}
		if err := syncDir(root, filepath.Dir(p)); err != nil {
			return err
		}
	}
	return nil
}
func syncDir(root *os.Root, p string) error {
	d, err := root.Open(p)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
func (f Files) Next(ref string) (uint64, error) {
	root, err := f.open()
	if err != nil {
		return 0, err
	}
	defer root.Close()
	if err := ensure(root, ref); err != nil {
		return 0, err
	}
	d, err := root.Open("s3/" + ref)
	if err != nil {
		return 0, ErrStorage
	}
	defer d.Close()
	entries, err := d.ReadDir(-1)
	if err != nil {
		return 0, ErrStorage
	}
	var max uint64
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".stage-") {
			continue
		}
		prefix, suffix, ok := strings.Cut(name, ".")
		if !ok || (suffix != "env" && suffix != "json") || !strings.HasPrefix(prefix, "v") {
			return 0, ErrStorage
		}
		n, err := strconv.ParseUint(prefix[1:], 10, 64)
		if err != nil || n == 0 || strconv.FormatUint(n, 10) != prefix[1:] {
			return 0, ErrStorage
		}
		info, err := root.Lstat("s3/" + ref + "/" + name)
		if err != nil || !protected(info, false, 0600) {
			return 0, ErrStorage
		}
		if n > max {
			max = n
		}
	}
	if max == ^uint64(0) {
		return 0, ErrStorage
	}
	return max + 1, nil
}
func immutable(root *os.Root, path string, data []byte) error {
	// A private same-directory staging file becomes visible only after fsync.
	parent := filepath.Dir(path)
	staged := parent + "/.stage-" + rand.Text()
	stage, err := root.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(staged)
	if err := stage.Chmod(0600); err != nil {
		stage.Close()
		return err
	}
	_, err = stage.Write(data)
	err = errors.Join(err, stage.Sync(), stage.Close())
	if err != nil {
		return err
	}
	// Link refuses an existing version; rename would overwrite an immutable file.
	if err := root.Link(staged, path); err != nil {
		return err
	}
	if err := root.Remove(staged); err != nil {
		return err
	}
	return syncDir(root, parent)
}
func (f Files) install(r Receipt, env []byte) error {
	root, err := f.open()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := ensure(root, r.Scope.CredentialRef); err != nil {
		return err
	}
	metadata, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := immutable(root, r.File, env); err != nil {
		return err
	}
	// An env without its receipt is reserved but unusable, never adopted on retry.
	return immutable(root, fileName(r.Scope.CredentialRef, r.Version, "json"), metadata)
}
func (f Files) Receipt(ref string, n uint64) (Receipt, error) {
	var receipt Receipt
	root, err := f.open()
	if err != nil {
		return receipt, err
	}
	defer root.Close()
	if err := ensure(root, ref); err != nil {
		return receipt, err
	}
	path := fileName(ref, n, "json")
	info, err := root.Lstat(path)
	if err != nil || !protected(info, false, 0600) {
		return receipt, ErrStorage
	}
	fd, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return receipt, ErrStorage
	}
	defer fd.Close()
	actual, err := fd.Stat()
	if err != nil || !os.SameFile(info, actual) || !protected(actual, false, 0600) || actual.Size() > 8192 {
		return receipt, ErrStorage
	}
	b, err := io.ReadAll(io.LimitReader(fd, 8193))
	if err != nil || len(b) > 8192 {
		return receipt, ErrStorage
	}
	receipt, err = DecodeReceipt(b)
	if err != nil {
		return Receipt{}, ErrStorage
	}
	if !receipt.Valid() || receipt.Scope.CredentialRef != ref || receipt.Version != n {
		return Receipt{}, ErrStorage
	}
	info, err = root.Lstat(receipt.File)
	if err != nil || !protected(info, false, 0600) {
		return Receipt{}, ErrStorage
	}
	return receipt, nil
}
func (f Files) locked(ctx context.Context, fn func() error) error {
	root, err := f.open()
	if err != nil {
		return err
	}
	defer root.Close()
	fd, err := root.OpenFile(".delivery.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return ErrStorage
	}
	defer fd.Close()
	info, err := fd.Stat()
	if err != nil || !protected(info, false, 0600) {
		return ErrStorage
	}
	for {
		err = syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return ErrStorage
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer syscall.Flock(int(fd.Fd()), syscall.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

// VersionPath is relative to the runner's credentials directory.
func VersionPath(ref string, version uint64) (string, error) {
	if !namePattern.MatchString(ref) || version == 0 {
		return "", ErrInvalid
	}
	return fileName(ref, version, "env"), nil
}
func (f Files) Path(ref string, version uint64) (string, error) {
	relative, err := VersionPath(ref, version)
	if err != nil || !filepath.IsAbs(f.Root) || filepath.Clean(f.Root) != f.Root {
		return "", ErrInvalid
	}
	return filepath.Join(f.Root, relative), nil
}
func (f Files) Read(ref string, version uint64) (Credentials, error) {
	path, err := VersionPath(ref, version)
	if err != nil {
		return Credentials{}, err
	}
	root, err := f.open()
	if err != nil {
		return Credentials{}, err
	}
	defer root.Close()
	dir, err := root.OpenFile("s3", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return Credentials{}, ErrStorage
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !protected(info, true, 0700) {
		return Credentials{}, ErrStorage
	}
	fd, err := unix.Openat(int(dir.Fd()), ref, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return Credentials{}, ErrStorage
	}
	parent := os.NewFile(uintptr(fd), ref)
	defer parent.Close()
	info, err = parent.Stat()
	if err != nil || !protected(info, true, 0700) {
		return Credentials{}, ErrStorage
	}
	fd, err = unix.Openat(int(parent.Fd()), filepath.Base(path), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return Credentials{}, ErrStorage
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !protected(info, false, 0600) || info.Size() > PacketLimit {
		return Credentials{}, ErrStorage
	}
	b, err := io.ReadAll(io.LimitReader(file, PacketLimit+1))
	defer clear(b)
	if err != nil || len(b) > PacketLimit {
		return Credentials{}, ErrStorage
	}
	return parseEnvironment(b)
}
func (f Files) ReadPath(path string) (Credentials, error) {
	relative, err := filepath.Rel(f.Root, path)
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return Credentials{}, ErrInvalid
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 3 || parts[0] != "s3" {
		return Credentials{}, ErrInvalid
	}
	name := parts[2]
	if !strings.HasPrefix(name, "v") || !strings.HasSuffix(name, ".env") {
		return Credentials{}, ErrInvalid
	}
	version, err := strconv.ParseUint(strings.TrimSuffix(name[1:], ".env"), 10, 64)
	if err != nil {
		return Credentials{}, ErrInvalid
	}
	expected, err := f.Path(parts[1], version)
	if err != nil || expected != path {
		return Credentials{}, ErrInvalid
	}
	return f.Read(parts[1], version)
}
