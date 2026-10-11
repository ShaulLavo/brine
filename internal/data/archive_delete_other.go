//go:build !linux

package data

import "context"

func InspectArchiveDeletion(ArchiveTree, AppIncarnationID, ArchiveID) (bool, error) {
	return false, ErrInvalid
}
func DeleteArchiveTree(context.Context, ArchiveTree, AppIncarnationID, ArchiveID, ArchiveDeletionParent, bool) error {
	return ErrInvalid
}

func CaptureArchiveDeletionParent(ArchiveTree, AppIncarnationID, ArchiveID) (ArchiveDeletionParent, error) {
	return ArchiveDeletionParent{}, ErrInvalid
}
func InspectArchiveDeletionParent(ArchiveTree, AppIncarnationID, ArchiveID, ArchiveDeletionParent) (bool, error) {
	return false, ErrInvalid
}
