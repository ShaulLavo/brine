//go:build linux

package data

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// RenameArchiveTree never replaces an existing archive, even an empty directory.
// An error after rename is unknown until the journaled paths are inspected.
func RenameArchiveTree(t ArchiveTree, inc AppIncarnationID, id ArchiveID) (err error) {
	if err = VerifyUnarchivedTree(t, inc, id); err != nil {
		return err
	}
	root, err := os.OpenRoot(string(t.Root))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if err = root.Mkdir("archives", 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err = verifyPrivateTree(string(t.Root), filepath.Join(string(t.Root), "archives")); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	// #nosec G115 -- A valid OS file descriptor fits the syscall's signed descriptor type.
	fd := int(directory.Fd())
	if err = unix.Renameat2(fd, t.SourceRelative, fd, t.ArchiveRelative, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	for _, name := range []string{"apps", "archives"} {
		parent, openErr := root.Open(name)
		if openErr != nil {
			return openErr
		}
		if err = errors.Join(parent.Sync(), parent.Close()); err != nil {
			return err
		}
	}
	if err = directory.Sync(); err != nil {
		return err
	}
	return VerifyArchivedTree(t, inc, id)
}
