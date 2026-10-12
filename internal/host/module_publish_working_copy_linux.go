//go:build linux

package host

import (
	"context"
	"errors"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// preparePublicationWorkingCopy leaves the authenticated download unchanged.
// A partial working directory belongs to this attempt and is never overwritten.
func preparePublicationWorkingCopy(ctx context.Context, source *nativeRestoredSource) (err error) {
	if source == nil || source.unchanged(ctx) != nil || source.root != source.extracted.Root() {
		return errEngineSource
	}
	if err = source.parent.Mkdir("working", 0700); err != nil {
		return errEngineSource
	}
	working, err := source.parent.OpenRoot("working")
	if err != nil {
		return errEngineSource
	}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, working.Close())
		}
	}()
	for _, member := range source.seal.members {
		if member.Mode&unix.S_IFMT == unix.S_IFDIR {
			err = working.Mkdir(member.Name, 0700)
		} else {
			err = copyEngineFile(ctx, source.root, working, member)
		}
		if err != nil {
			return err
		}
	}
	if err = syncNativeRestoreTarget(ctx, working, source.limits); err != nil {
		return err
	}
	if err = syncEngineDirectory(source.parent, "."); err != nil {
		return err
	}
	seal, err := sealEngineRoot(ctx, working, source.limits)
	if err != nil || !sameEngineContents(source.seal, seal) || !source.extracted.MatchesWorkingCopy(ctx, source.parent, "working", working) || source.unchanged(ctx) != nil {
		return errEngineSource
	}
	source.root, source.seal = working, seal
	source.path = filepath.Join(filepath.Dir(source.path), "working")
	complete = true
	return nil
}
