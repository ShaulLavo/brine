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
	for _, file := range []struct{ path, content string }{{reconcileUnitPath, reconcileUnit}, {reconcileGeneratorPath, reconcileGenerator}} {
		if err := protectedParents(file.path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
		ok, err := h.fileMatches(file.path, hash([]byte(file.content)))
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (h *host) installBootReconcile(ctx context.Context) error {
	for _, dir := range []string{filepath.Dir(reconcileUnitPath), filepath.Dir(reconcileGeneratorPath)} {
		if _, owned := h.r.Dirs[dir]; owned {
			if err := h.dir(dir, 0755, 0, 0); err != nil {
				return err
			}
			continue
		}
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			if err := h.dir(dir, 0755, 0, 0); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || st.Uid != 0 || info.Mode().Perm()&0022 != 0 {
			return errors.New("boot recovery parent must be a protected root-owned directory")
		}
	}
	if err := h.file(ctx, reconcileUnitPath, []byte(reconcileUnit), 0644, false); err != nil {
		return err
	}
	return h.file(ctx, reconcileGeneratorPath, []byte(reconcileGenerator), 0755, false)
}

func (h *host) undoBootReconcile(context.Context) error {
	// Withdraw activation before removing the service. Never remove an unrecorded
	// file or adopt a changed unit/generator: restoreFile verifies ownership/hash.
	for _, path := range []string{reconcileGeneratorPath, reconcileUnitPath} {
		if err := h.restoreFile(path); err != nil {
			return err
		}
	}
	for _, path := range []string{filepath.Dir(reconcileGeneratorPath), filepath.Dir(reconcileUnitPath)} {
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
