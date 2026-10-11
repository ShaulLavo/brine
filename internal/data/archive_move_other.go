//go:build !linux

package data

func RenameArchiveTree(ArchiveTree, AppIncarnationID, ArchiveID) error { return ErrInvalid }

func SyncArchivedTree(ArchiveTree, AppIncarnationID, ArchiveID) error { return ErrInvalid }
