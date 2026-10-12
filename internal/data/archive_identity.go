package data

import "path/filepath"

func VerifyArchiveAllocation(b SQLiteDatabaseBinding, a AllocationReceipt) error {
	return verifyArchiveAllocationIdentity(b.Root, b.IncarnationID, b.DatabaseID, b.RelativeDirectory, a)
}

func VerifyNativeArchiveAllocation(b SpacetimeDBBinding, a AllocationReceipt) error {
	if b.Validate() != nil {
		return ErrInvalid
	}
	return verifyArchiveAllocationIdentity(b.Root, b.IncarnationID, b.DatabaseID, b.RelativeDirectory, a)
}

func verifyArchiveAllocationIdentity(root PersistentRoot, incarnation AppIncarnationID, database DatabaseID, directoryName string, a AllocationReceipt) error {
	relative, err := RelativeDirectory(incarnation, database)
	if err != nil || !ValidRoot(string(root)) || directoryName != relative || !a.Valid() || a.DatabaseID != database || a.IncarnationID != incarnation {
		return ErrInvalid
	}
	directory := filepath.Join(string(root), relative)
	if err := verifyPrivateTree(string(root), directory); err != nil {
		return err
	}
	rootDevice, rootInode, err := directoryIdentity(string(root))
	if err != nil {
		return err
	}
	device, inode, err := directoryIdentity(directory)
	if err != nil {
		return err
	}
	if rootDevice != a.RootDevice || rootInode != a.RootInode || device != a.DirectoryDevice || inode != a.DirectoryInode {
		return ErrInvalid
	}
	return nil
}
