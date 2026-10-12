//go:build linux

package host

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/data"
)

func discardNativeWorkspace(parent *os.Root, path string) error {
	if parent == nil || !filepath.IsAbs(path) || !data.ValidID(filepath.Base(path)) {
		return errEngineSource
	}
	identity, err := nativeRestoreDirectoryIdentity(parent, ".")
	if err != nil {
		return err
	}
	base, err := engineOpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = base.Close() }()
	current, err := nativeRestoreDirectoryIdentity(base, filepath.Base(path))
	if err != nil || !sameNativeRestoreDirectory(identity, current) {
		return errEngineSource
	}
	return errors.Join(parent.Close(), base.RemoveAll(filepath.Base(path)), syncEngineDirectory(base, "."))
}
