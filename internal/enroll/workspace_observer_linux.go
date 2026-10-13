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
	if _, owned := h.r.Files[workspaceobserve.UnitPath]; !owned {
		if _, err := os.Lstat(workspaceobserve.UnitPath); !os.IsNotExist(err) {
			return false, errors.New("unowned workspace observer unit")
		}
		return false, nil
	}
	matched, err := h.fileMatches(workspaceobserve.UnitPath, hash([]byte(workspaceobserve.Unit)))
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
	if err := protectedParents(workspaceobserve.UnitPath); err != nil {
		return err
	}
	if err := h.file(ctx, workspaceobserve.UnitPath, []byte(workspaceobserve.Unit), 0644, false); err != nil {
		return err
	}
	if info, err := os.Lstat(workspaceobserve.GrantPath); os.IsNotExist(err) {
		if err := h.file(ctx, workspaceobserve.GrantPath, []byte(workspaceobserve.DefaultGrant), 0600, false); err != nil {
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
	if _, owned := h.r.Files[workspaceobserve.UnitPath]; !owned {
		return nil
	}
	matches, err := h.fileMatches(workspaceobserve.UnitPath, hash([]byte(workspaceobserve.Unit)))
	if err != nil || !matches {
		return errors.New("workspace observer unit changed")
	}
	if _, err := h.run(ctx, true, "systemctl", "stop", workspaceobserve.UnitName); err != nil {
		return err
	}
	if _, err := h.run(ctx, true, "systemctl", "disable", workspaceobserve.UnitName); err != nil {
		return err
	}
	if err := h.restoreFile(workspaceobserve.UnitPath); err != nil {
		return err
	}
	if _, owned := h.r.Files[workspaceobserve.GrantPath]; owned {
		matches, err := h.fileMatches(workspaceobserve.GrantPath, hash([]byte(workspaceobserve.DefaultGrant)))
		if err != nil {
			return err
		}
		if matches {
			if err = h.restoreFile(workspaceobserve.GrantPath); err != nil {
				return err
			}
		}
	}
	_, err = h.run(ctx, true, "systemctl", "daemon-reload")
	return err
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
