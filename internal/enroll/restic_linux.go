//go:build linux

package enroll

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/restic"
)

var resticDirectories = []string{"/opt/brine", "/opt/brine/restic", "/opt/brine/restic/" + restic.Version}

func (h *host) checkRestic(ctx context.Context) (bool, error) {
	asset, err := ResticAsset(runtime.GOARCH)
	if err != nil {
		return false, err
	}
	recorded, ok := h.r.Files[restic.ToolPath]
	if !ok {
		if _, err := os.Lstat(restic.ToolPath); !errors.Is(err, os.ErrNotExist) {
			return false, errors.New("unrecorded restic executable refused")
		}
		return false, nil
	}
	if recorded.Hash != asset.BinarySHA256 || recorded.Mode != 0755 || protectedParents(restic.ToolPath) != nil {
		return false, errors.New("restic protected pin differs")
	}
	matched, err := h.fileMatches(restic.ToolPath, asset.BinarySHA256)
	if err != nil || !matched {
		return false, err
	}
	info, err := os.Lstat(restic.ToolPath)
	if err != nil || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return false, errors.New("restic executable link drift")
	}
	for _, directory := range resticDirectories {
		if err := h.checkResticDirectory(directory); err != nil {
			return false, err
		}
	}
	if err := h.resticVersion(ctx, restic.ToolPath); err != nil {
		return false, err
	}
	if _, err := os.Lstat(recordDir + "/restic-stage"); !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

func (h *host) checkResticDirectory(path string) error {
	if owned, ok := h.r.Dirs[path]; ok {
		_, err := h.checkDirectory(path, owned)
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0755 || info.Sys().(*syscall.Stat_t).Uid != 0 || protectedParents(path+"/check") != nil {
		return errors.New("restic directory ownership refused")
	}
	return nil
}

func (h *host) installRestic(ctx context.Context) error {
	asset, err := ResticAsset(runtime.GOARCH)
	if err != nil {
		return err
	}
	binary, err := downloadRestic(ctx, asset)
	if err != nil {
		return err
	}
	for _, directory := range resticDirectories {
		if err := h.prepareResticDirectory(directory); err != nil {
			return err
		}
	}
	if protectedParents(restic.ToolPath) != nil {
		return errors.New("restic protected parents refused")
	}
	stage := recordDir + "/restic-stage"
	if err := h.file(ctx, stage, binary, 0755, false); err != nil {
		return err
	}
	if err := h.resticVersion(ctx, stage); err != nil {
		return err
	}
	if err := h.file(ctx, restic.ToolPath, binary, 0755, false); err != nil {
		return err
	}
	return h.restoreFile(stage)
}

func (h *host) prepareResticDirectory(path string) error {
	if _, recorded := h.r.Dirs[path]; recorded {
		return h.dir(path, 0755, 0, 0)
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return h.dir(path, 0755, 0, 0)
	}
	return h.checkResticDirectory(path)
}

func (h *host) resticVersion(ctx context.Context, path string) error {
	output, err := h.exec.Execute(ctx, localexec.Command{Path: path, Args: []string{"version"}, Timeout: 5 * time.Second, OutputLimit: 1024})
	expected := "restic " + restic.Version + " compiled with go1.26.4 on linux/" + runtime.GOARCH
	if err != nil || output.Truncated || strings.TrimSpace(output.Stdout) != expected {
		return errors.New("restic executable version refused")
	}
	return nil
}

func (h *host) undoRestic(context.Context) error {
	if err := h.restoreFile(recordDir + "/restic-stage"); err != nil {
		return err
	}
	if err := h.restoreFile(restic.ToolPath); err != nil {
		return err
	}
	for _, path := range []string{resticDirectories[2], resticDirectories[1]} {
		if err := h.removeResticDirectory(path); err != nil {
			return err
		}
	}
	return nil
}

func (h *host) removeResticDirectory(path string) error {
	owned, recorded := h.r.Dirs[path]
	if !recorded {
		return nil
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if _, err := h.checkDirectory(path, owned); err != nil || protectedParents(path) != nil {
		return errors.New("restic undo directory ownership drift")
	}
	if err := os.Remove(path); err != nil {
		return errors.New("restic undo contains unowned files")
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(parent.Sync(), parent.Close())
}
