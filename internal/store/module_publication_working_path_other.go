//go:build !linux

package store

import "os"

func checkPublicationWorkingPath(*os.Root, *os.Root, os.FileInfo, string) error {
	return ErrConflict
}
