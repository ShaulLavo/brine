package data

import "path/filepath"

func VerifyArchiveAllocation(b DatabaseBinding, a AllocationReceipt) error {
	relative, err := RelativeDirectory(b.IncarnationID, b.DatabaseID)
	if err != nil || !ValidRoot(string(b.Root)) || b.RelativeDirectory != relative || !a.Valid() || a.DatabaseID != b.DatabaseID || a.IncarnationID != b.IncarnationID {
		return ErrInvalid
	}
	directory := filepath.Join(string(b.Root), relative)
	if err := verifyPrivateTree(string(b.Root), directory); err != nil {
		return err
	}
	rootDevice, rootInode, err := directoryIdentity(string(b.Root))
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
