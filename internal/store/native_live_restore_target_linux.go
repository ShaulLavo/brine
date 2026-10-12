//go:build linux

package store

import (
	"context"
	"errors"
	"path"

	"github.com/ShaulLavo/brine/internal/data"
	"golang.org/x/sys/unix"
)

func checkNativeRestoreTargetAnchors(ctx context.Context, capability *NativeLiveRestoreExecution, verified nativeLiveRestoreVerifiedRecord, observed NativeRestoreTargetObservation) error {
	if checkNativeRestoreAnchors(ctx, capability, observed.Download, NativeRestoreObservation{Parent: observed.Parent, TargetTree: verified.TargetTree}) != nil {
		return ErrConflict
	}
	binding := capability.Binding()
	base, err := unix.Openat2(unix.AT_FDCWD, string(binding.Root), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = unix.Close(base) }()
	root, err := nativeRestoreStatDirectory(base)
	if err != nil {
		return err
	}
	if checkNativeRestoreTreeAnchor(base, root, binding.RelativeDirectory, observed.OldTree) != nil || checkNativeRestoreTreeAnchor(base, root, path.Join("transitions", capability.Attempt().TransitionID, "staged"), observed.TargetTree) != nil {
		return ErrConflict
	}
	return ctx.Err()
}

func checkNativeRestoreTreeAnchor(base int, root unix.Statx_t, name string, tree data.EngineTreeSeal) error {
	parent, err := nativeRestoreOpenDirectory(base, path.Dir(name))
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = unix.Close(parent) }()
	p, err := nativeRestoreStatDirectory(parent)
	if err != nil || p.Mnt_id != root.Mnt_id || nativeRestoreDirectoryID(p) != tree.Parent {
		return ErrConflict
	}
	fd, err := nativeRestoreOpenDirectory(parent, path.Base(name))
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = unix.Close(fd) }()
	leaf, err := nativeRestoreStatDirectory(fd)
	if err != nil || leaf.Mnt_id != root.Mnt_id || nativeRestoreDirectoryID(leaf) != tree.Directory || nativeRestoreDirectoryID(root) != tree.Root {
		return ErrConflict
	}
	return nil
}

func checkNativeRestoreTransitionAnchors(ctx context.Context, capability *NativeRestoreTransition, input data.EngineTransition, observed NativeRestoreTransitionObservation) error {
	binding := capability.binding
	base, err := unix.Openat2(unix.AT_FDCWD, string(binding.Root), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = unix.Close(base) }()
	root, err := nativeRestoreStatDirectory(base)
	if err != nil {
		return err
	}
	parent := path.Join("transitions", capability.attempt.TransitionID)
	locations := []data.EngineLocation{observed.Layout.Live, observed.Layout.Staged, observed.Layout.Preserved}
	names := []string{binding.RelativeDirectory, path.Join(parent, "staged"), path.Join(parent, "original")}
	for i, location := range locations {
		if tree, present := location.Tree(); present {
			if checkNativeRestoreTreeAnchor(base, root, names[i], tree) != nil {
				return ErrConflict
			}
			continue
		}
		if location.Kind() != "absent" {
			return ErrConflict
		}
		fd, openErr := nativeRestoreOpenDirectory(base, names[i])
		if openErr == nil {
			_ = unix.Close(fd)
			return ErrConflict
		}
		if !errors.Is(openErr, unix.ENOENT) {
			return ErrConflict
		}
	}
	return ctx.Err()
}
