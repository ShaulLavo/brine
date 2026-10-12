//go:build linux

package store

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/archive"
	"golang.org/x/sys/unix"
	"path"
)

func (s *Store) checkNativeArchiveMoveObservation(ctx context.Context, p archive.Plan, tree archive.NativeTransitionTree, o NativeArchiveTreeObservation) error {
	for _, variant := range p.Candidate.Databases {
		d, ok := variant.Native()
		if !ok || !nativeArchiveDatabaseHasTree(d, tree.TransitionID) {
			continue
		}
		return s.checkNativeArchiveMovedTree(ctx, p, d, tree, o)
	}
	return ErrConflict
}
func nativeArchiveDatabaseHasTree(d archive.NativeDatabase, id string) bool {
	for _, tree := range d.TransitionTrees {
		if tree.TransitionID == id {
			return true
		}
	}
	return false
}
func (s *Store) checkNativeArchiveMovedTree(ctx context.Context, p archive.Plan, d archive.NativeDatabase, tree archive.NativeTransitionTree, o NativeArchiveTreeObservation) error {
	if o.Root == nil || !o.Lifetime.HoldsLifetime(s.dir, d.Binding.BindingID) || o.Tree.Root != tree.Seal.Root || o.Tree.Directory != tree.Seal.Directory || o.Tree.ContentsSHA256 != tree.Seal.ContentsSHA256 {
		return ErrConflict
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, string(d.Binding.Root), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = unix.Close(fd) }()
	root, err := nativeRestoreStatDirectory(fd)
	if err != nil {
		return err
	}
	held, err := o.Root.Open(".")
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = held.Close() }()
	heldRoot, err := nativeRestoreStatDirectory(int(held.Fd()))
	if err != nil || nativeRestoreDirectoryID(heldRoot) != nativeRestoreDirectoryID(root) {
		return ErrConflict
	}
	name := path.Join("archives", string(p.ArchiveID), "native-transitions", tree.TransitionID)
	if checkNativeRestoreTreeAnchor(fd, root, name, o.Tree) != nil {
		return ErrConflict
	}
	source := path.Join("transitions", tree.TransitionID)
	file, openErr := nativeRestoreOpenDirectory(fd, source)
	if openErr == nil {
		_ = unix.Close(file)
		return ErrConflict
	}
	if !errors.Is(openErr, unix.ENOENT) {
		return ErrConflict
	}
	return ctx.Err()
}
