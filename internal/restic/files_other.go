//go:build !linux && !darwin

package restic

import "os"

func privateDirectory(string) error                 { return ErrInvalid }
func safeNode(os.FileInfo, os.FileInfo) bool        { return false }
func protectedNode(os.FileInfo, bool) bool          { return false }
func openMember(*os.Root, string) (*os.File, error) { return nil, ErrInvalid }
func openArchive(string) (*os.File, error)          { return nil, ErrInvalid }
