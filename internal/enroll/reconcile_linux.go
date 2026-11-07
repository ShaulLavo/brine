//go:build linux

package enroll

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

const reconcileUnitPath = "/etc/systemd/user/brine-reconcile.service"
const reconcileGeneratorPath = "/etc/systemd/user-generators/brine-reconcile"

const reconcileUnit = `[Unit]
Description=Reconcile interrupted Brine operations
ConditionUser=brine

[Service]
Type=oneshot
ExecStart=/usr/local/bin/brine host reconcile --json
Environment=PATH=/usr/local/bin:/usr/bin:/bin
TimeoutStartSec=15min
Restart=no
`

// A user generator activates only the enrolled account. Unlike global enable,
// it does not add a dependency to any other user's default target. Its output
// is disposable user-manager state, never a privileged enrollment mutation.
// Every path argument is quoted; there is no interpolation into shell commands.
const reconcileGenerator = `#!/bin/sh
set -eu
[ "$(/usr/bin/id -un)" = brine ] || exit 0
[ "$#" -eq 3 ] || exit 1
/usr/bin/mkdir -p -- "$1/default.target.wants"
/usr/bin/ln -s -- /etc/systemd/user/brine-reconcile.service "$1/default.target.wants/brine-reconcile.service"
`

func (h *host) checkBootReconcile(context.Context) (bool, error) {
	artifacts := enrollmentArtifacts()
	for _, file := range []enrollmentArtifact{artifacts[artifactReconcileUnit], artifacts[artifactReconcileGenerator]} {
		err := protectedParents(file.path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		ok, err := h.fileMatches(file.path, hash(file.render(h)))
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (h *host) installBootReconcile(ctx context.Context) error {
	artifacts := enrollmentArtifacts()
	files := []enrollmentArtifact{artifacts[artifactReconcileUnit], artifacts[artifactReconcileGenerator]}
	for _, file := range files {
		if err := h.prepareBootReconcileDirectory(filepath.Dir(file.path)); err != nil {
			return err
		}
	}
	for _, file := range files {
		if err := h.file(ctx, file.path, file.render(h), file.mode, false); err != nil {
			return err
		}
	}
	return nil
}

func (h *host) prepareBootReconcileDirectory(dir string) error {
	if _, owned := h.r.Dirs[dir]; owned {
		return h.dir(dir, 0755, 0, 0)
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return h.dir(dir, 0755, 0, 0)
	}
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || st.Uid != 0 || info.Mode().Perm()&0022 != 0 {
		return errors.New("boot recovery parent must be a protected root-owned directory")
	}
	return nil
}

func (h *host) undoBootReconcile(context.Context) error {
	artifacts := enrollmentArtifacts()
	unit, generator := artifacts[artifactReconcileUnit], artifacts[artifactReconcileGenerator]
	// Withdraw activation before removing the service. Never remove an unrecorded
	// file or adopt a changed unit/generator: restoreFile verifies ownership/hash.
	for _, path := range []string{generator.path, unit.path} {
		if err := h.restoreFile(path); err != nil {
			return err
		}
	}
	for _, path := range []string{filepath.Dir(generator.path), filepath.Dir(unit.path)} {
		recorded, owned := h.r.Dirs[path]
		if !owned {
			continue
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		ok, err := h.checkDirectory(path, recorded)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("boot recovery directory provenance unknown")
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}
