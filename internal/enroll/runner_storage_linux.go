//go:build linux

package enroll

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/runnerstorage"
	"os"
	"strconv"
)

func (h *host) checkRunnerStorage(context.Context) (bool, error) {
	old, ok := h.r.Files[runnerstorage.Path]
	if !ok {
		return false, nil
	}
	if old.Hash != hash([]byte(runnerstorage.Config)) || old.Mode != 0644 {
		return false, runnerstorage.ErrProfile
	}
	matches, err := h.fileMatches(runnerstorage.Path, old.Hash)
	if err != nil || !matches {
		return false, err
	}
	return runnerstorage.CheckLocal() == nil, nil
}
func (h *host) installRunnerStorage(ctx context.Context) error {
	if err := h.dir("/etc/brine", 0755, 0, 0); err != nil {
		return err
	}
	if err := h.file(ctx, runnerstorage.Path, []byte(runnerstorage.Config), 0644, false); err != nil {
		return err
	}
	return runnerstorage.CheckLocal()
}
func (h *host) undoRunnerStorage(context.Context) error {
	if err := h.restoreFile(runnerstorage.Path); err != nil {
		return err
	}
	owned, ok := h.r.Dirs["/etc/brine"]
	if !ok {
		return nil
	}
	return h.removeRunnerStorageDirectory("/etc/brine", owned)
}

func (h *host) removeRunnerStorageDirectory(path string, owned ownedDir) error {
	if _, err := h.checkDirectory(path, owned); err != nil {
		return err
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (h *host) qualifyRunnerStorage(ctx context.Context) error {
	if runnerstorage.CheckLocal() != nil {
		return runnerstorage.ErrProfile
	}
	prefix := []string{"-u", "brine", "--", "/usr/bin/env", runnerstorage.Environment, "HOME=" + home, "XDG_CONFIG_HOME=" + home + "/.config", "XDG_DATA_HOME=" + home + "/.local/share", "XDG_CACHE_HOME=" + home + "/.cache", "XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(h.r.UID), "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/" + strconv.Itoa(h.r.UID) + "/bus"}
	version, err := h.run(ctx, false, "runuser", append(prefix, runnerstorage.FusePath, "--version")...)
	if err != nil || version.Truncated || runnerstorage.ValidateVersion(version.Stdout) != nil {
		return errors.Join(runnerstorage.ErrProfile, err)
	}
	info, err := h.run(ctx, false, "runuser", append(prefix, "/usr/bin/podman", "info", "--format=json")...)
	if err != nil || info.Truncated || runnerstorage.ValidateInfo([]byte(info.Stdout)) != nil {
		return errors.Join(runnerstorage.ErrProfile, err)
	}
	return nil
}
