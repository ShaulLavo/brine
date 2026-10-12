//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

// nativeRestoreStage reserves one download below the actual20 operation's
// private parent. A path or decoded claim cannot construct this stage.
type nativeRestoreStage struct {
	state          *store.Store
	claim          *store.NativeLiveRestoreExecution
	source         engineSealedCheckpoint
	base           *os.Root
	root           *os.Root
	path           string
	parentName     string
	downloadName   string
	stagedName     string
	nonce          string
	pointID        string
	rootIdentity   engineTreeMember
	parentIdentity engineTreeMember
}

func newNativeRestoreStage(ctx context.Context, state *store.Store, claim *store.NativeLiveRestoreExecution, source engineSealedCheckpoint) (stage *nativeRestoreStage, err error) {
	if state == nil || state.CheckNativeLiveRestoreExecution(ctx, claim) != nil || source.state != state || source.unchanged(ctx) != nil || source.stopped.PointID() != claim.Attempt().PreparedPointID || source.checkpoint.SHA256() != claim.CheckpointSHA256() || source.original.hash != claim.SourceSealSHA256() {
		return nil, errEngineSource
	}
	binding := claim.Binding()
	base, err := engineOpenRoot(string(binding.Root))
	if err != nil {
		return nil, err
	}
	stage = &nativeRestoreStage{state: state, claim: claim, source: source, base: base, parentName: filepath.Join("transitions", claim.Attempt().TransitionID), downloadName: "download", stagedName: "staged", nonce: claim.IntentSHA256(), pointID: claim.Attempt().SelectedPointID}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, stage.close())
			stage = nil
		}
	}()
	stage.rootIdentity, err = nativeRestoreDirectoryIdentity(base, ".")
	if err != nil {
		return stage, err
	}
	if err = engineMakePrivateDirectories(base, "transitions"); err != nil {
		return stage, err
	}
	if err = syncEngineDirectory(base, "."); err != nil {
		return stage, err
	}
	if err = base.Mkdir(stage.parentName, 0700); err != nil {
		return stage, errEngineSource
	}
	if err = stage.writeOwner(ctx); err != nil {
		return stage, err
	}
	if err = syncEngineDirectory(base, "transitions"); err != nil {
		return stage, err
	}
	stage.parentIdentity, err = nativeRestoreDirectoryIdentity(base, stage.parentName)
	if err != nil || !sameNativeRestoreMount(stage.rootIdentity, stage.parentIdentity) {
		return stage, errEngineSource
	}
	file, err := engineOpenMember(base, stage.parentName, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return stage, err
	}
	defer func() { _ = file.Close() }()
	stage.root, err = os.OpenRoot("/proc/self/fd/" + strconv.Itoa(int(file.Fd())))
	if err != nil {
		return stage, err
	}
	stage.path = filepath.Join(string(binding.Root), stage.parentName, stage.downloadName)
	if err = stage.unchanged(ctx); err != nil {
		return stage, err
	}
	if err = stage.downloadAbsent(ctx); err != nil {
		return stage, err
	}
	complete = true
	return stage, nil
}

func (s *nativeRestoreStage) writeOwner(ctx context.Context) error {
	file, err := engineOpenMember(s.base, filepath.Join(s.parentName, ".brine-restore-owner"), unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY, 0600)
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = file.Close() }()
	written, err := file.Write([]byte(s.nonce))
	if err != nil || written != len(s.nonce) || ctx.Err() != nil || file.Sync() != nil {
		return errEngineSource
	}
	return syncEngineDirectory(s.base, s.parentName)
}

func (s *nativeRestoreStage) unchanged(ctx context.Context) error {
	if s == nil || s.base == nil || s.root == nil || s.state == nil || s.state.CheckNativeLiveRestoreRetainedSource(ctx, s.claim) != nil || s.source.unchanged(ctx) != nil {
		return errEngineSource
	}
	opened, err := engineOpenRoot(string(s.claim.Binding().Root))
	if err != nil {
		return err
	}
	defer func() { _ = opened.Close() }()
	root, err := nativeRestoreDirectoryIdentity(opened, ".")
	if err != nil || !sameNativeRestoreDirectory(root, s.rootIdentity) {
		return errEngineSource
	}
	parent, err := nativeRestoreDirectoryIdentity(opened, s.parentName)
	if err != nil || !sameNativeRestoreDirectory(parent, s.parentIdentity) {
		return errEngineSource
	}
	held, err := nativeRestoreDirectoryIdentity(s.root, ".")
	if err != nil || !sameNativeRestoreDirectory(held, parent) {
		return errEngineSource
	}
	file, err := engineOpenMember(opened, filepath.Join(s.parentName, ".brine-restore-owner"), unix.O_RDONLY, 0)
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = file.Close() }()
	member, err := engineMember(file, "")
	if err != nil || member.Size != int64(len(s.nonce)) {
		return errEngineSource
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(len(s.nonce))+1))
	if err != nil || !bytes.Equal(raw, []byte(s.nonce)) {
		return errEngineSource
	}
	return ctx.Err()
}

// A pending extraction is inspected through its retained intent. It cannot
// reacquire an absent-destination reservation by calling the downloader again.
func (s *nativeRestoreStage) downloadAbsent(ctx context.Context) error {
	if s.unchanged(ctx) != nil {
		return errEngineSource
	}
	for _, name := range []string{s.downloadName, s.stagedName} {
		if _, err := s.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return errEngineSource
		}
	}
	return ctx.Err()
}

func nativeRestoreDirectoryIdentity(root *os.Root, name string) (engineTreeMember, error) {
	file, err := engineOpenMember(root, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return engineTreeMember{}, err
	}
	defer func() { _ = file.Close() }()
	return engineSourceMember(file, "")
}

func sameNativeRestoreMount(a, b engineTreeMember) bool {
	return a.Device == b.Device && a.MountID == b.MountID
}
func sameNativeRestoreDirectory(a, b engineTreeMember) bool {
	return sameNativeRestoreMount(a, b) && a.Inode == b.Inode && a.UID == b.UID && a.Mode == b.Mode
}

// Closing never deletes partial downloads or the retained operation parent.
func (s *nativeRestoreStage) close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.root != nil {
		err = s.root.Close()
		s.root = nil
	}
	if s.base != nil {
		err = errors.Join(err, s.base.Close())
		s.base = nil
	}
	return err
}
