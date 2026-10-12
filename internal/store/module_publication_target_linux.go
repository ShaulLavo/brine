//go:build linux

package store

import (
	"context"
	"errors"
	"math"
	"os"
	"path"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"golang.org/x/sys/unix"
)

func checkPublicationTargetAnchors(ctx context.Context, capability *PublicationTargetPreparation, observed PublicationTargetObservation) error {
	if observed.Parent == nil || observed.Staged == nil || capability.attempt.restored.unchanged(ctx) != nil {
		return ErrConflict
	}
	binding := capability.intent.Binding
	base, err := unix.Openat2(unix.AT_FDCWD, string(binding.Root), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = unix.Close(base) }()
	root, err := nativeRestoreStatDirectory(base)
	if err != nil {
		return err
	}
	parent := path.Join("transitions", capability.intent.TransitionID)
	if checkNativeRestoreTreeAnchor(base, root, binding.RelativeDirectory, observed.OldTree) != nil || checkNativeRestoreTreeAnchor(base, root, path.Join(parent, "staged"), observed.TargetTree) != nil {
		return ErrConflict
	}
	parentID, err := publicationHeldRootIdentity(observed.Parent)
	stagedID, stagedErr := publicationHeldRootIdentity(observed.Staged)
	if err != nil || stagedErr != nil || parentID != observed.TargetTree.Parent || stagedID != observed.TargetTree.Directory {
		return ErrConflict
	}
	parentFile, err := observed.Parent.Open(".")
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = parentFile.Close() }()
	if parentFile.Fd() > math.MaxInt32 {
		return ErrConflict
	}
	marker := nativecapture.CheckpointMember{Path: ".brine-publication-owner", Bytes: uint64(len(capability.intentSHA)), SHA256: publicationStorageSHA256([]byte(capability.intentSHA))}
	if checkPublicationCheckpointMember(ctx, int(parentFile.Fd()), root.Mnt_id, marker) != nil {
		return ErrConflict
	}
	return ctx.Err()
}

func checkPublicationTransitionAnchors(ctx context.Context, capability *PublicationTransition, observed data.EngineLayoutObservation) error {
	if capability == nil || capability.preparation == nil || !capability.active(ctx, capability.preparation.state) || ctx.Err() != nil {
		return ErrConflict
	}
	binding := capability.preparation.intent.Binding
	base, err := unix.Openat2(unix.AT_FDCWD, string(binding.Root), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = unix.Close(base) }()
	root, err := nativeRestoreStatDirectory(base)
	if err != nil {
		return err
	}
	parent := path.Join("transitions", capability.preparation.intent.TransitionID)
	locations := []data.EngineLocation{observed.Live, observed.Staged, observed.Preserved}
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

func publicationHeldRootIdentity(root *os.Root) (data.EngineDirectoryIdentity, error) {
	if root == nil {
		return data.EngineDirectoryIdentity{}, ErrConflict
	}
	file, err := root.Open(".")
	if err != nil {
		return data.EngineDirectoryIdentity{}, err
	}
	defer func() { _ = file.Close() }()
	if file.Fd() > math.MaxInt32 {
		return data.EngineDirectoryIdentity{}, ErrConflict
	}
	stat, err := nativeRestoreStatDirectory(int(file.Fd()))
	return nativeRestoreDirectoryID(stat), err
}

func checkPublicationSourcePreservationAnchors(ctx context.Context, permit EnginePermit, effects []PublicationEffect, observed PublicationSourcePreservationObservation) error {
	if observed.OriginalRoot == nil || permit.Allocation == nil || ctx.Err() != nil {
		return ErrConflict
	}
	binding := permit.Reserved.Database
	base, err := unix.Openat2(unix.AT_FDCWD, string(binding.Root), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = unix.Close(base) }()
	root, err := nativeRestoreStatDirectory(base)
	if err != nil {
		return err
	}
	a := permit.Allocation
	if nativeRestoreDirectoryID(root) != (data.EngineDirectoryIdentity{Device: a.RootDevice, Inode: a.RootInode}) {
		return ErrConflict
	}
	fd, err := nativeRestoreOpenDirectory(base, binding.RelativeDirectory)
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = unix.Close(fd) }()
	physical, err := nativeRestoreStatDirectory(fd)
	held, heldErr := publicationHeldRootIdentity(observed.OriginalRoot)
	if err != nil || heldErr != nil || nativeRestoreDirectoryID(physical) != held || held != (data.EngineDirectoryIdentity{Device: a.DirectoryDevice, Inode: a.DirectoryInode}) || physical.Mnt_id != root.Mnt_id {
		return ErrConflict
	}
	if len(effects) == 0 {
		if observed.WorkingRoot != nil {
			return ErrConflict
		}
		return ctx.Err()
	}
	workingID, err := publicationHeldRootIdentity(observed.WorkingRoot)
	if err != nil || workingID != effects[0].RootIdentity || workingID == held {
		return ErrConflict
	}
	work, err := unix.Openat2(unix.AT_FDCWD, effects[0].RootPath, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return ErrConflict
	}
	defer func() { _ = unix.Close(work) }()
	stat, err := nativeRestoreStatDirectory(work)
	if err != nil || nativeRestoreDirectoryID(stat) != workingID || stat.Mnt_id != root.Mnt_id {
		return ErrConflict
	}
	return ctx.Err()
}
