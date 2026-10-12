//go:build linux

package host

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

func (h archiveHost) ArchiveNativeTrees(ctx context.Context, p archive.Plan) error {
	for _, variant := range p.Candidate.Databases {
		native, ok := variant.Native()
		if !ok {
			continue
		}
		if err := h.archiveNativeDatabaseTrees(ctx, p, native); err != nil {
			return err
		}
	}
	return nil
}

func (h archiveHost) archiveNativeDatabaseTrees(ctx context.Context, p archive.Plan, native archive.NativeDatabase) error {
	for _, tree := range native.TransitionTrees {
		if err := h.moveNativeArchiveTree(ctx, p, native.Binding, tree); err != nil {
			return err
		}
	}
	return nil
}

func nativeArchivePreparedPointID(p archive.Plan, binding data.ReplicaBindingID) string {
	for _, variant := range p.Candidate.Databases {
		native, ok := variant.Native()
		if !ok || native.Binding.BindingID != binding {
			continue
		}
		return nativeArchiveDatabasePointID(native)
	}
	return ""
}
func nativeArchiveDatabasePointID(native archive.NativeDatabase) string {
	for _, ref := range native.Dependencies {
		if ref.Kind == data.EnginePreparedPointEvidence {
			return ref.ID
		}
	}
	return ""
}

func (h archiveHost) moveNativeArchiveTree(ctx context.Context, p archive.Plan, binding data.SpacetimeDBBinding, expected archive.NativeTransitionTree) error {
	lifetime := h.nativeLifetimes[binding.BindingID]
	if !lifetime.HoldsLifetime(h.stateRoot, binding.BindingID) {
		return archive.ErrRefused
	}
	pointID := nativeArchivePreparedPointID(p, binding.BindingID)
	point, err := h.service.Store.ReadNativePreparedPoint(ctx, pointID)
	if err != nil {
		return err
	}
	preparation, err := h.service.Store.AcquireEnginePreparation(ctx, binding.BindingID, point.FenceID, point.FenceOwnerOpID)
	if err != nil {
		return err
	}
	defer func() { _ = preparation.Close() }()
	move, fresh, err := h.service.Store.BeginNativeArchiveTreeMove(ctx, p.ArchiveID, expected.TransitionID, preparation, lifetime)
	if err != nil || move.Tree != expected {
		return archive.ErrRefused
	}
	base, err := engineOpenRoot(string(binding.Root))
	if err != nil {
		return err
	}
	defer func() { _ = base.Close() }()
	destinationParent := filepath.Join("archives", string(p.ArchiveID), "native-transitions")
	if err = engineMakePrivateDirectories(base, destinationParent); err != nil {
		return err
	}
	if err = syncEngineDirectory(base, filepath.Dir(destinationParent)); err != nil {
		return err
	}
	source := filepath.Join("transitions", expected.TransitionID)
	destination := filepath.Join(destinationParent, expected.TransitionID)
	observer := engineTransitionTree{limits: nativeArchiveTreeLimits()}
	before, err := observer.inspectLocation(ctx, base, source)
	if err != nil {
		return err
	}
	after, err := observer.inspectLocation(ctx, base, destination)
	if err != nil {
		return err
	}
	sourceSeal, sourcePresent := before.Tree()
	destSeal, destPresent := after.Tree()
	if sourcePresent {
		if !fresh || move.Stage != "intent" || sourceSeal != expected.Seal || destPresent || after.Kind() != "absent" {
			return archive.ErrRecovery
		}
		if err = renameNativeArchiveTree(base, source, destination, expected.Seal); err != nil {
			return archive.ErrRecovery
		}
	} else {
		if before.Kind() != "absent" || !destPresent || !nativeArchiveMovedSeal(expected.Seal, destSeal) {
			return archive.ErrRecovery
		}
	}
	if err = syncEngineDirectory(base, "transitions"); err != nil {
		return err
	}
	if err = syncEngineDirectory(base, destinationParent); err != nil {
		return err
	}
	before, err = observer.inspectLocation(ctx, base, source)
	if err != nil || before.Kind() != "absent" {
		return archive.ErrRecovery
	}
	after, err = observer.inspectLocation(ctx, base, destination)
	destSeal, destPresent = after.Tree()
	if err != nil || !destPresent || !nativeArchiveMovedSeal(expected.Seal, destSeal) {
		return archive.ErrRecovery
	}
	if move.Stage == "moved" {
		return nil
	}
	return h.service.Store.CompleteNativeArchiveTreeMove(ctx, move, store.NativeArchiveTreeObservation{Root: base, Tree: destSeal, Lifetime: lifetime, Preparation: preparation})
}

func nativeArchiveMovedSeal(old, moved data.EngineTreeSeal) bool {
	return old.Root == moved.Root && old.Directory == moved.Directory && old.ContentsSHA256 == moved.ContentsSHA256
}

func renameNativeArchiveTree(base *os.Root, source, destination string, expected data.EngineTreeSeal) error {
	from, err := engineOpenMember(base, filepath.Dir(source), unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = from.Close() }()
	to, err := engineOpenMember(base, filepath.Dir(destination), unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = to.Close() }()
	original, err := nativeRestoreDirectoryIdentity(base, source)
	parent, perr := engineSourceMember(from, "")
	targetParent, terr := engineSourceMember(to, "")
	if err != nil || perr != nil || terr != nil || engineTransitionDirectory(original) != expected.Directory || engineTransitionDirectory(parent) != expected.Parent || targetParent.MountID != parent.MountID || targetParent.Device != parent.Device {
		return errEngineSource
	}
	if err = unix.Renameat2(int(from.Fd()), filepath.Base(source), int(to.Fd()), filepath.Base(destination), unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	return errors.Join(from.Sync(), to.Sync())
}
