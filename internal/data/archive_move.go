package data

import (
	"errors"
	"os"
	"path/filepath"
)

func VerifyUnarchivedTree(t ArchiveTree, inc AppIncarnationID, id ArchiveID) (err error) {
	current, err := CaptureArchiveTree(t.Root, inc, id)
	if err != nil || current != t {
		return ErrInvalid
	}
	root, err := os.OpenRoot(string(t.Root))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if _, err = root.Lstat("archives"); err == nil {
		if err = verifyPrivateTree(string(t.Root), filepath.Join(string(t.Root), "archives")); err != nil {
			return err
		}
		device, _, identityErr := directoryIdentity(filepath.Join(string(t.Root), "archives"))
		if identityErr != nil || device != t.RootDevice {
			return ErrInvalid
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err = root.Lstat(t.ArchiveRelative); !os.IsNotExist(err) {
		return ErrInvalid
	}
	return nil
}
func VerifyArchivedTree(t ArchiveTree, inc AppIncarnationID, id ArchiveID) (err error) {
	if !t.Valid(inc, id) {
		return ErrInvalid
	}
	root, err := os.OpenRoot(string(t.Root))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if _, err = root.Lstat(t.SourceRelative); !os.IsNotExist(err) {
		return ErrInvalid
	}
	archived := filepath.Join(string(t.Root), t.ArchiveRelative)
	if err = verifyPrivateTree(string(t.Root), archived); err != nil {
		return err
	}
	dev, inode, err := directoryIdentity(string(t.Root))
	if err != nil || dev != t.RootDevice || inode != t.RootInode {
		return ErrInvalid
	}
	dev, inode, err = directoryIdentity(archived)
	if err != nil || dev != t.SourceDevice || inode != t.SourceInode {
		return ErrInvalid
	}
	return nil
}
func VerifyArchivedAllocation(b SQLiteDatabaseBinding, a AllocationReceipt, id ArchiveID) error {
	return verifyArchivedAllocationIdentity(b.Root, b.IncarnationID, b.DatabaseID, b.RelativeDirectory, a, id)
}

func VerifyNativeArchivedAllocation(b SpacetimeDBBinding, a AllocationReceipt, id ArchiveID) error {
	if b.Validate() != nil {
		return ErrInvalid
	}
	return verifyArchivedAllocationIdentity(b.Root, b.IncarnationID, b.DatabaseID, b.RelativeDirectory, a, id)
}

func verifyArchivedAllocationIdentity(root PersistentRoot, incarnation AppIncarnationID, database DatabaseID, directoryName string, a AllocationReceipt, id ArchiveID) error {
	if !ValidID(string(id)) || !a.Valid() || a.DatabaseID != database || a.IncarnationID != incarnation {
		return ErrInvalid
	}
	relative, err := RelativeDirectory(incarnation, database)
	if err != nil || relative != directoryName {
		return ErrInvalid
	}
	archived := filepath.Join(string(root), "archives", string(id), "databases", string(database))
	if err = verifyPrivateTree(string(root), archived); err != nil {
		return err
	}
	dev, inode, err := directoryIdentity(string(root))
	if err != nil || dev != a.RootDevice || inode != a.RootInode {
		return ErrInvalid
	}
	dev, inode, err = directoryIdentity(archived)
	if err != nil || dev != a.DirectoryDevice || inode != a.DirectoryInode {
		return ErrInvalid
	}
	return nil
}
