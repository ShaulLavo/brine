package data

import (
	"path/filepath"
	"time"
)

type ArchiveTree struct {
	Root            PersistentRoot `json:"root"`
	SourceRelative  string         `json:"source_relative"`
	ArchiveRelative string         `json:"archive_relative"`
	RootDevice      uint64         `json:"root_device"`
	RootInode       uint64         `json:"root_inode"`
	SourceDevice    uint64         `json:"source_device"`
	SourceInode     uint64         `json:"source_inode"`
}

func (t ArchiveTree) Valid(incarnation AppIncarnationID, archive ArchiveID) bool {
	return ValidRoot(string(t.Root)) && ValidID(string(incarnation)) && ValidID(string(archive)) && t.SourceRelative == filepath.Join("apps", string(incarnation)) && t.ArchiveRelative == filepath.Join("archives", string(archive)) && t.RootDevice != 0 && t.RootInode != 0 && t.SourceDevice == t.RootDevice && t.SourceInode != 0
}

func CaptureArchiveTree(root PersistentRoot, incarnation AppIncarnationID, archive ArchiveID) (ArchiveTree, error) {
	t := ArchiveTree{Root: root, SourceRelative: filepath.Join("apps", string(incarnation)), ArchiveRelative: filepath.Join("archives", string(archive))}
	if !ValidRoot(string(root)) || !ValidID(string(incarnation)) || !ValidID(string(archive)) {
		return t, ErrInvalid
	}
	if err := verifyPrivateTree(string(root), filepath.Join(string(root), t.SourceRelative)); err != nil {
		return t, err
	}
	var err error
	t.RootDevice, t.RootInode, err = directoryIdentity(string(root))
	if err != nil {
		return t, err
	}
	t.SourceDevice, t.SourceInode, err = directoryIdentity(filepath.Join(string(root), t.SourceRelative))
	if err != nil || !t.Valid(incarnation, archive) {
		return t, ErrInvalid
	}
	return t, nil
}

func FreshArchiveAllocation(b SQLiteDatabaseBinding, original AllocationReceipt) (AllocationReceipt, SQLiteSchemaObservation, error) {
	fresh, err := CaptureAllocation(b)
	observation := SQLiteSchemaObservation{DatabaseID: b.DatabaseID, State: AllocatedEmpty, Marker: EmptyMarker, CatalogSHA256: EmptyCatalogSHA256, ObservedAt: time.Now().UTC()}
	if err != nil || !original.Valid() || fresh.DatabaseID != original.DatabaseID || fresh.IncarnationID != original.IncarnationID || fresh.RootDevice != original.RootDevice || fresh.RootInode != original.RootInode || fresh.DirectoryDevice != original.DirectoryDevice || fresh.DirectoryInode != original.DirectoryInode {
		return AllocationReceipt{}, SQLiteSchemaObservation{}, ErrInvalid
	}
	return fresh, observation, nil
}
