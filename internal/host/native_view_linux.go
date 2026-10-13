//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"golang.org/x/sys/unix"
)

type nativeViewState uint8

const (
	nativeViewPrepared nativeViewState = iota + 1
	nativeViewReading
	nativeViewDisposed
)

// A view owns only independently copied selected history. It carries no
// checkpoint, original-source, process-disposal or mutation authority.
type nativeReadView struct {
	parent    *os.Root
	root      *os.Root
	path      string
	name      string
	replica   string
	nonce     string
	selection nativecapture.Selection
	seal      engineTreeSeal
	limits    engineCopyLimits
	state     nativeViewState
	staging   *engineStagingParent
}

func materializeNativeSelection(ctx context.Context, source *os.Root, staging string, selection nativecapture.Selection, nonce string) (view *nativeReadView, err error) {
	if source == nil || !data.ValidID(nonce) || selection.Kind() != "logs" && selection.Kind() != "snapshot" || len(selection.Members()) == 0 {
		return nil, errEngineSource
	}
	parent, err := engineOpenRoot(staging)
	if err != nil {
		return nil, err
	}
	view = &nativeReadView{parent: parent, nonce: nonce, selection: selection, path: filepath.Join(staging, nonce), replica: "replicas/" + strconv.FormatUint(selection.Scope().ReplicaID, 10), limits: engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16}}
	complete := false
	defer func() {
		if complete {
			return
		}
		err = errors.Join(err, view.close())
		view = nil
	}()
	if err = parent.Mkdir(nonce, 0700); err != nil {
		return view, errEngineSource
	}
	view.name = nonce
	view.root, err = parent.OpenRoot(nonce)
	if err != nil {
		return view, errEngineSource
	}
	if err = view.root.MkdirAll(view.replica+"/clog", 0700); err != nil {
		return view, errEngineSource
	}
	for _, member := range selection.Members() {
		if err = view.copyMember(ctx, source, member); err != nil {
			return view, err
		}
	}
	if err = view.root.WriteFile(view.replica+"/db.lock", nil, 0600); err != nil {
		return view, errEngineSource
	}
	view.seal, err = sealEngineRoot(ctx, view.root, view.limits)
	if err != nil || syncEngineDirectory(parent, nonce) != nil {
		return view, errEngineSource
	}
	view.state = nativeViewPrepared
	complete = true
	return view, nil
}

func (v *nativeReadView) copyMember(ctx context.Context, source *os.Root, recorded nativecapture.Member) (err error) {
	if !strings.HasPrefix(recorded.Path, v.replica+"/") {
		return errEngineSource
	}
	file, err := engineOpenMember(source, recorded.Path, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	member, err := engineMember(file, recorded.Path)
	if err != nil || member.Mode&unix.S_IFMT != unix.S_IFREG || member.Size < 0 || uint64(member.Size) != recorded.Bytes {
		return errEngineSource
	}
	member.SHA256 = recorded.SHA256
	if err := v.root.MkdirAll(filepath.Dir(recorded.Path), 0700); err != nil {
		return errEngineSource
	}
	return copyEngineFile(ctx, source, v.root, member)
}

func (v *nativeReadView) unchanged(ctx context.Context) error {
	if v == nil || v.root == nil || v.name == "" {
		return errEngineSource
	}
	seal, err := sealEngineRoot(ctx, v.root, v.limits)
	if err != nil || seal.hash != v.seal.hash {
		return errEngineSource
	}
	fresh, err := engineOpenRoot(v.path)
	if err != nil {
		return err
	}
	defer func() { _ = fresh.Close() }()
	seal, err = sealEngineRoot(ctx, fresh, v.limits)
	if err != nil || seal.hash != v.seal.hash {
		return errEngineSource
	}
	return nil
}

func (v *nativeReadView) close() error {
	if v == nil {
		return nil
	}
	if v.state == nativeViewReading {
		return errEngineSource
	}
	if v.name != "" && !v.currentDirectory() {
		return errEngineSource
	}
	var err error
	if v.root != nil {
		err = v.root.Close()
		v.root = nil
	}
	if v.parent != nil && v.name != "" {
		err = errors.Join(err, v.parent.RemoveAll(v.name))
		v.name = ""
	}
	if v.parent != nil {
		err = errors.Join(err, v.parent.Close())
		v.parent = nil
	}
	return errors.Join(err, v.staging.close())
}

func (v *nativeReadView) currentDirectory() bool {
	if v.root == nil || v.parent == nil {
		return false
	}
	original, err := v.root.Stat(".")
	current, currentErr := v.parent.Lstat(v.name)
	return err == nil && currentErr == nil && current.IsDir() && os.SameFile(original, current)
}
