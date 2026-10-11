//go:build linux

package data

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// RenameArchiveTree never replaces an existing archive, even an empty directory.
// An error after rename is unknown until the journaled paths are inspected.
func RenameArchiveTree(t ArchiveTree, inc AppIncarnationID, id ArchiveID) error {
	return renameArchiveTree(t, inc, id, unix.Renameat2)
}

func renameArchiveTree(t ArchiveTree, inc AppIncarnationID, id ArchiveID, move func(int, string, int, string, uint) error) (err error) {
	if err = VerifyUnarchivedTree(t, inc, id); err != nil {
		return err
	}
	root, err := os.OpenRoot(string(t.Root))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	rootInfo, err := directory.Stat()
	if err != nil {
		return err
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok || !rootInfo.IsDir() || !owned(rootInfo) || rootInfo.Mode().Perm() != 0700 || uint64(rootStat.Dev) != t.RootDevice || uint64(rootStat.Ino) != t.RootInode {
		return ErrInvalid
	}
	// #nosec G115 -- A valid OS file descriptor fits the syscall descriptor type.
	rootFD := int(directory.Fd())
	if err = unix.Mkdirat(rootFD, "archives", 0700); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	// os.Root.Rename uses ordinary rename semantics and may replace a target.
	// Pin no-follow parent handles and give renameat2 validated leaf names only.
	parents := make([]*os.File, 0, 2)
	defer func() {
		for _, parent := range parents {
			err = errors.Join(err, parent.Close())
		}
	}()
	for _, name := range []string{"apps", "archives"} {
		fd, openErr := unix.Openat(rootFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return openErr
		}
		parent := os.NewFile(uintptr(fd), name)
		parents = append(parents, parent)
		info, statErr := parent.Stat()
		if statErr != nil {
			return statErr
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || !owned(info) || info.Mode().Perm() != 0700 || uint64(stat.Dev) != t.RootDevice {
			return ErrInvalid
		}
		var entry unix.Stat_t
		if err = unix.Fstatat(rootFD, name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if entry.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(entry.Dev) != uint64(stat.Dev) || uint64(entry.Ino) != uint64(stat.Ino) {
			return ErrInvalid
		}
	}
	// #nosec G115 -- Valid OS file descriptors fit the syscall descriptor type.
	fromFD, toFD := int(parents[0].Fd()), int(parents[1].Fd())
	var source unix.Stat_t
	if err = unix.Fstatat(fromFD, string(inc), &source, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if source.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(source.Dev) != t.SourceDevice || uint64(source.Ino) != t.SourceInode {
		return ErrInvalid
	}
	var target unix.Stat_t
	if err = unix.Fstatat(toFD, string(id), &target, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return ErrInvalid
	}
	if err = move(fromFD, string(inc), toFD, string(id), unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	for _, parent := range parents {
		if err = parent.Sync(); err != nil {
			return err
		}
	}
	if err = directory.Sync(); err != nil {
		return err
	}
	// Rebinding either namespace name cannot redirect the syscall, but leaves an
	// interrupted move requiring inspection instead of a success receipt.
	for _, parent := range parents {
		info, statErr := parent.Stat()
		if statErr != nil {
			return statErr
		}
		var entry unix.Stat_t
		if err = unix.Fstatat(rootFD, parent.Name(), &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || entry.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(entry.Dev) != uint64(stat.Dev) || uint64(entry.Ino) != uint64(stat.Ino) {
			return ErrInvalid
		}
	}
	return VerifyArchivedTree(t, inc, id)
}

// SyncArchivedTree settles a rename whose effect is visible but whose parent
// fsync/receipt was interrupted. It never moves or replaces a namespace entry.
func SyncArchivedTree(t ArchiveTree, inc AppIncarnationID, id ArchiveID) (err error) {
	if err = VerifyArchivedTree(t, inc, id); err != nil {
		return err
	}
	root, err := os.OpenRoot(string(t.Root))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	rootInfo, statErr := directory.Stat()
	if statErr != nil {
		return statErr
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok || !owned(rootInfo) || rootInfo.Mode().Perm() != 0700 || uint64(rootStat.Dev) != t.RootDevice || uint64(rootStat.Ino) != t.RootInode {
		return ErrInvalid
	}
	// #nosec G115 -- Valid OS file descriptors fit syscall descriptor types.
	rootFD := int(directory.Fd())
	for _, name := range []string{"apps", "archives"} {
		fd, openErr := unix.Openat(rootFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return openErr
		}
		parent := os.NewFile(uintptr(fd), name)
		syncErr := parent.Sync()
		info, statErr := parent.Stat()
		closeErr := parent.Close()
		if err = errors.Join(syncErr, statErr, closeErr); err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		var entry unix.Stat_t
		if !ok || !owned(info) || info.Mode().Perm() != 0700 || uint64(stat.Dev) != t.RootDevice {
			return ErrInvalid
		}
		if err = unix.Fstatat(rootFD, name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if entry.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(entry.Dev) != uint64(stat.Dev) || uint64(entry.Ino) != uint64(stat.Ino) {
			return ErrInvalid
		}
	}
	if err = directory.Sync(); err != nil {
		return err
	}
	return VerifyArchivedTree(t, inc, id)
}
