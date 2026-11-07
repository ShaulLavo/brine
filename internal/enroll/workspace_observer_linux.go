//go:build linux

package enroll

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"

	"github.com/ShaulLavo/brine/internal/workspaceobserve"
)

func (h *host) checkWorkspaceObserver(ctx context.Context) (bool, error) {
	unit := enrollmentArtifacts()[artifactObserverUnit]
	if _, owned := h.r.Files[unit.path]; !owned {
		if _, err := os.Lstat(unit.path); !os.IsNotExist(err) {
			return false, errors.New("unowned workspace observer unit")
		}
		return false, nil
	}
	matched, err := h.fileMatches(unit.path, hash(unit.render(h)))
	if err != nil || !matched {
		return false, err
	}
	if _, err = readObserverGrant(); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	observed, err := h.run(ctx, false, "systemctl", "show", workspaceobserve.UnitName, "--property=LoadState", "--property=ActiveState", "--property=UnitFileState", "--property=FragmentPath")
	if err != nil {
		return false, err
	}
	return observerInstalled(observed.Stdout), nil
}
func (h *host) installWorkspaceObserver(ctx context.Context) error {
	artifacts := enrollmentArtifacts()
	unit, grant := artifacts[artifactObserverUnit], artifacts[artifactObserverGrant]
	if err := protectedParents(unit.path); err != nil {
		return err
	}
	if err := h.file(ctx, unit.path, unit.render(h), unit.mode, false); err != nil {
		return err
	}
	if info, err := os.Lstat(grant.path); os.IsNotExist(err) {
		if err := h.file(ctx, grant.path, grant.render(h), grant.mode, false); err != nil {
			return err
		}
	} else if err != nil || !trustedObserverGrant(info) {
		return errors.New("unprotected workspace observer grant")
	} else if _, err = readObserverGrant(); err != nil {
		return err
	}
	if _, err := h.run(ctx, true, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	_, err := h.run(ctx, true, "systemctl", "enable", "--now", workspaceobserve.UnitName)
	return err
}
func trustedObserverGrant(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && (info.Mode().Perm() == 0600 || info.Mode().Perm() == 0644) && stat.Uid == 0 && stat.Nlink == 1
}
func (h *host) undoWorkspaceObserver(ctx context.Context) error {
	artifacts := enrollmentArtifacts()
	unit := artifacts[artifactObserverUnit]
	if _, owned := h.r.Files[unit.path]; !owned {
		return nil
	}
	matches, err := h.fileMatches(unit.path, hash(unit.render(h)))
	if err != nil || !matches {
		return errors.New("workspace observer unit changed")
	}
	if _, err := h.run(ctx, true, "systemctl", "stop", workspaceobserve.UnitName); err != nil {
		return err
	}
	if _, err := h.run(ctx, true, "systemctl", "disable", workspaceobserve.UnitName); err != nil {
		return err
	}
	if err := h.restoreFile(unit.path); err != nil {
		return err
	}
	if err := h.restoreObserverGrant(artifacts[artifactObserverGrant]); err != nil {
		return err
	}
	_, err = h.run(ctx, true, "systemctl", "daemon-reload")
	return err
}

func (h *host) restoreObserverGrant(grant enrollmentArtifact) error {
	if _, owned := h.r.Files[grant.path]; !owned {
		return nil
	}
	matches, err := h.fileMatches(grant.path, hash(grant.render(h)))
	if err != nil || !matches {
		return err
	}
	return h.restoreFile(grant.path)
}

func readObserverGrant() ([]byte, error) {
	if err := protectedParents(workspaceobserve.GrantPath); err != nil {
		return nil, err
	}
	info, err := os.Lstat(workspaceobserve.GrantPath)
	if err != nil {
		return nil, err
	}
	if !trustedObserverGrant(info) || info.Size() > 4096 {
		return nil, errors.New("unprotected workspace observer grant")
	}
	raw, err := os.ReadFile(workspaceobserve.GrantPath)
	after, afterErr := os.Lstat(workspaceobserve.GrantPath)
	if err != nil || afterErr != nil || !os.SameFile(info, after) {
		return nil, errors.New("workspace observer grant changed")
	}
	if _, err = workspaceobserve.ParseGrant(raw); err != nil {
		return nil, err
	}
	return raw, nil
}
func observerInstalled(raw string) bool {
	expected := map[string]string{"LoadState": "loaded", "ActiveState": "active", "UnitFileState": "enabled", "FragmentPath": workspaceobserve.UnitPath}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || seen[key] || expected[key] != value {
			return false
		}
		seen[key] = true
	}
	return len(seen) == len(expected)
}
