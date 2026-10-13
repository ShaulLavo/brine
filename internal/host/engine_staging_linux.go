//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
)

type engineStagingParent struct {
	base    *os.Root
	root    *os.Root
	path    string
	created bool
}

func prepareEngineStaging(ctx context.Context, stateRoot string) (_ *engineStagingParent, err error) {
	if ctx.Err() != nil {
		return nil, errEngineSource
	}
	if _, err := data.InspectRoot(stateRoot); err != nil {
		return nil, errEngineSource
	}
	base, err := engineOpenRoot(stateRoot)
	if err != nil {
		return nil, err
	}
	staging := &engineStagingParent{base: base, path: filepath.Join(stateRoot, "staging")}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, staging.close())
		}
	}()
	_, statErr := base.Lstat("staging")
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, errEngineSource
	}
	if err := engineMakePrivateDirectories(base, "staging"); err != nil {
		return nil, err
	}
	staging.created = os.IsNotExist(statErr)
	staging.root, err = base.OpenRoot("staging")
	if err != nil || syncEngineDirectory(base, ".") != nil || ctx.Err() != nil {
		return nil, errEngineSource
	}
	complete = true
	return staging, nil
}

func (s *engineStagingParent) close() error {
	if s == nil || s.base == nil {
		return nil
	}
	var err error
	if s.created {
		err = s.removeCreated()
	}
	if s.root != nil {
		err = errors.Join(err, s.root.Close())
		s.root = nil
	}
	err = errors.Join(err, s.base.Close())
	s.base = nil
	return err
}

func (s *engineStagingParent) removeCreated() error {
	if s.root == nil {
		return errEngineSource
	}
	held, err := s.root.Stat(".")
	current, currentErr := s.base.Lstat("staging")
	if err != nil || currentErr != nil || !os.SameFile(held, current) || engineDirectoryChain(s.base, "staging") != nil {
		return errEngineSource
	}
	if err := s.base.Remove("staging"); err != nil {
		return err
	}
	return syncEngineDirectory(s.base, ".")
}

func copyEngineStateSource(ctx context.Context, binding data.SpacetimeDBBinding, stateRoot, name string, limits engineCopyLimits) (*engineSourceCopy, error) {
	parent, err := prepareEngineStaging(ctx, stateRoot)
	if err != nil {
		return nil, err
	}
	copied, err := copyEngineSource(ctx, binding, parent.path, name, limits)
	if err != nil {
		return nil, errors.Join(err, parent.close())
	}
	copied.staging = parent
	return copied, nil
}

func materializeNativeStateSelection(ctx context.Context, source *os.Root, stateRoot string, selection nativecapture.Selection, nonce string) (*nativeReadView, error) {
	parent, err := prepareEngineStaging(ctx, stateRoot)
	if err != nil {
		return nil, err
	}
	view, err := materializeNativeSelection(ctx, source, parent.path, selection, nonce)
	if err != nil {
		return nil, errors.Join(err, parent.close())
	}
	view.staging = parent
	return view, nil
}
