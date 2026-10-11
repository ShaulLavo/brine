//go:build !linux

package data

func RenameArchiveTree(ArchiveTree, AppIncarnationID, ArchiveID) error { return ErrInvalid }
