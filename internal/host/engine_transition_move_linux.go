//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path"

	"github.com/ShaulLavo/brine/internal/data"
	"golang.org/x/sys/unix"
)

type engineTransitionMove string

const (
	enginePreserveOriginal engineTransitionMove = "preserve_original"
	enginePromoteTarget    engineTransitionMove = "promote_target"
)

func (t *engineTransitionTree) move(ctx context.Context, cursor data.EngineTransitionCursor, movement engineTransitionMove) error {
	return t.moveWith(ctx, cursor, movement, unix.Renameat2, func(file *os.File) error { return file.Sync() })
}

func (t *engineTransitionTree) moveWith(ctx context.Context, cursor data.EngineTransitionCursor, movement engineTransitionMove, rename func(int, string, int, string, uint) error, sync func(*os.File) error) error {
	if !t.active() || cursor.ID != t.input.Common().Scope.ID || cursor.InputSHA256 != t.input.SHA256() {
		return errEngineSource
	}
	before, err := t.inspect(ctx)
	if err != nil {
		return err
	}
	state := data.ClassifyEngineTransitionLayout(t.input, before)
	from, to, wanted, err := t.moveNames(cursor, movement, state)
	if err != nil {
		return err
	}
	root, err := engineOpenRoot(string(t.binding.Root))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	base, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = base.Close() }()
	rootMember, err := engineSourceMember(base, ".")
	if err != nil {
		return err
	}
	source, destination, err := engineMoveParents(root, rootMember, from, to)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close(); _ = destination.Close() }()
	if err = t.checkMoveHandles(rootMember, source, destination, from, to, movement); err != nil || !t.active() || ctx.Err() != nil {
		return errEngineSource
	}
	if err = rename(int(source.Fd()), path.Base(from), int(destination.Fd()), path.Base(to), unix.RENAME_NOREPLACE); err != nil {
		return errors.Join(errEngineSource, err)
	}
	if err = sync(source); err != nil {
		return err
	}
	if err = sync(destination); err != nil {
		return err
	}
	if err = sync(base); err != nil {
		return err
	}
	after, err := t.inspect(ctx)
	if err != nil || data.ClassifyEngineTransitionLayout(t.input, after) != wanted {
		return errEngineSource
	}
	return nil
}

func (t *engineTransitionTree) checkMoveHandles(rootMember engineTreeMember, source, destination *os.File, from, to string, movement engineTransitionMove) error {
	c := t.input.Common()
	wanted := c.OldTree
	sourceParent, targetParent := c.OldTree.Parent, c.PreservedParent
	if movement == enginePromoteTarget {
		wanted = c.TargetTree
		sourceParent, targetParent = c.TargetTree.Parent, c.OldTree.Parent
	}
	sourceMember, sourceErr := engineSourceMember(source, "")
	targetMember, targetErr := engineSourceMember(destination, "")
	if sourceErr != nil || targetErr != nil || engineTransitionDirectory(rootMember) != c.OldTree.Root ||
		engineTransitionDirectory(sourceMember) != sourceParent || engineTransitionDirectory(targetMember) != targetParent {
		return errEngineSource
	}
	var fromStat, toStat unix.Stat_t
	if unix.Fstatat(int(source.Fd()), path.Base(from), &fromStat, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		fromStat.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(fromStat.Dev) != wanted.Directory.Device || uint64(fromStat.Ino) != wanted.Directory.Inode {
		return errEngineSource
	}
	if err := unix.Fstatat(int(destination.Fd()), path.Base(to), &toStat, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return errEngineSource
	}
	return nil
}

func engineMoveParents(root *os.Root, rootMember engineTreeMember, from, to string) (*os.File, *os.File, error) {
	source, err := openEngineTransitionParent(root, path.Dir(from), rootMember)
	if err != nil {
		return nil, nil, err
	}
	destination, err := openEngineTransitionParent(root, path.Dir(to), rootMember)
	if err != nil {
		_ = source.Close()
		return nil, nil, err
	}
	return source, destination, nil
}

func (t *engineTransitionTree) moveNames(cursor data.EngineTransitionCursor, movement engineTransitionMove, state data.EngineLayoutState) (string, string, data.EngineLayoutState, error) {
	live, staged, preserved := t.relativeNames()
	if movement == enginePreserveOriginal && cursor.Stage == data.EngineTransitionPreserveIntent && state == data.EngineLayoutPrepared {
		return live, preserved, data.EngineLayoutPreserved, nil
	}
	if movement == enginePromoteTarget && cursor.Stage == data.EngineTransitionPromoteIntent && state == data.EngineLayoutPreserved {
		return staged, live, data.EngineLayoutPromoted, nil
	}
	return "", "", data.EngineLayoutConflict, errEngineSource
}

// settle only fsyncs positively observed movement and never issues a rename.
func (t *engineTransitionTree) settle(ctx context.Context, cursor data.EngineTransitionCursor, movement engineTransitionMove) error {
	if !t.active() || cursor.ID != t.input.Common().Scope.ID || cursor.InputSHA256 != t.input.SHA256() {
		return errEngineSource
	}
	observed, err := t.inspect(ctx)
	if err != nil {
		return err
	}
	state := data.ClassifyEngineTransitionLayout(t.input, observed)
	live, staged, preserved := t.relativeNames()
	names := []string{path.Dir(live), path.Dir(preserved)}
	if movement == enginePromoteTarget && cursor.Stage == data.EngineTransitionPromoteIntent && state == data.EngineLayoutPromoted {
		names = []string{path.Dir(staged), path.Dir(live)}
	} else if movement != enginePreserveOriginal || cursor.Stage != data.EngineTransitionPreserveIntent || state != data.EngineLayoutPreserved {
		return errEngineSource
	}
	root, err := engineOpenRoot(string(t.binding.Root))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, parent := range names {
		if err = syncEngineDirectory(root, parent); err != nil {
			return err
		}
	}
	if err = syncEngineDirectory(root, "."); err != nil {
		return err
	}
	rechecked, err := t.inspect(ctx)
	if err != nil || data.ClassifyEngineTransitionLayout(t.input, rechecked) != state {
		return errEngineSource
	}
	return nil
}
