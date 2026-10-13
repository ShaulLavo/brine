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
	staging.created, err = engineMakePrivateDirectory(base, "staging")
	if err != nil {
		return nil, err
	}
	validated, err := base.Lstat("staging")
	if err != nil {
		return nil, errEngineSource
	}
	staging.root, err = base.OpenRoot("staging")
	if err != nil || syncEngineDirectory(base, ".") != nil || ctx.Err() != nil {
		return nil, errEngineSource
	}
	held, err := staging.root.Stat(".")
	current, currentErr := base.Lstat("staging")
	if err != nil || currentErr != nil || !os.SameFile(validated, held) || !os.SameFile(held, current) || engineDirectoryChain(base, "staging") != nil {
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
	if !s.current() {
		return errEngineSource
	}
	if err := s.base.Remove("staging"); err != nil {
		return err
	}
	return syncEngineDirectory(s.base, ".")
}

func (s *engineStagingParent) current() bool {
	if s.root == nil || s.base == nil {
		return false
	}
	held, err := s.root.Stat(".")
	current, currentErr := s.base.Lstat("staging")
	return err == nil && currentErr == nil && os.SameFile(held, current) && engineDirectoryChain(s.base, "staging") == nil
}

func openEngineStagingConsumer(path string, owned *engineStagingParent) (*os.Root, error) {
	if owned == nil {
		return engineOpenRoot(path)
	}
	if owned.path != path || !owned.current() {
		return nil, errEngineSource
	}
	root, err := owned.root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if !owned.current() {
		_ = root.Close()
		return nil, errEngineSource
	}
	return root, nil
}

func copyEngineStateSource(ctx context.Context, binding data.SpacetimeDBBinding, stateRoot, name string, limits engineCopyLimits) (*engineSourceCopy, error) {
	parent, err := prepareEngineStaging(ctx, stateRoot)
	if err != nil {
		return nil, err
	}
	copied, err := copyEngineSourceInParent(ctx, binding, parent.path, name, limits, parent)
	if err != nil {
		return nil, errors.Join(err, parent.close())
	}
	copied.staging = parent
	if !parent.current() {
		return nil, errors.Join(errEngineSource, copied.discard())
	}
	return copied, nil
}

func materializeNativeStateSelection(ctx context.Context, source *os.Root, stateRoot string, selection nativecapture.Selection, nonce string) (*nativeReadView, error) {
	parent, err := prepareEngineStaging(ctx, stateRoot)
	if err != nil {
		return nil, err
	}
	view, err := materializeNativeSelectionInParent(ctx, source, parent.path, selection, nonce, parent)
	if err != nil {
		return nil, errors.Join(err, parent.close())
	}
	view.staging = parent
	if !parent.current() {
		return nil, errors.Join(errEngineSource, view.close())
	}
	return view, nil
}
