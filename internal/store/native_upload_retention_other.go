//go:build !linux

package store

import (
	"github.com/ShaulLavo/brine/internal/data"
	"os"
)

func nativeUploadDirectoryIdentity(string, string, *os.Root) (data.EngineDirectoryIdentity, error) {
	return data.EngineDirectoryIdentity{}, ErrConflict
}
