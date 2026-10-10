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
)

var litestreamDirs = []string{"/opt/brine", "/opt/brine/litestream", "/opt/brine/litestream/0.5.17"}

func (h *host) checkLitestream(ctx context.Context) (bool, error) {
	recorded, ok := h.r.Files[LitestreamPath]
	if !ok {
		if _, err := os.Lstat(LitestreamPath); !errors.Is(err, os.ErrNotExist) {
			return false, errors.New("unrecorded Litestream install refused")
		}
		return false, nil
	}
	matched, err := h.fileMatches(LitestreamPath, recorded.Hash)
	if err != nil || !matched {
		return false, err
	}
	if err := protectedParents(LitestreamPath); err != nil {
		return false, err
	}
	info, err := os.Lstat(LitestreamPath)
	if err != nil || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return false, errors.New("Litestream executable link drift")
	}
	for _, path := range litestreamDirs {
		if owned, ok := h.r.Dirs[path]; ok {
			if _, err := h.checkDirectory(path, owned); err != nil {
				return false, err
			}
		}
	}

	out, err := h.exec.Execute(ctx, localexec.Command{Path: LitestreamPath, Args: []string{"version"}, Timeout: 5 * time.Second, OutputLimit: 1024})
	if err != nil || out.Truncated || strings.TrimSpace(out.Stdout) != "0.5.17" {
		return false, errors.New("installed Litestream version refused")
	}

	if _, recorded := h.r.Files[recordDir+"/litestream-stage"]; recorded {
		if _, err := os.Lstat(recordDir + "/litestream-stage"); err == nil {
			return false, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return true, nil
}
func (h *host) installLitestream(ctx context.Context) error {
	asset, err := LitestreamAsset(runtime.GOARCH)
	if err != nil {
		return err
	}
	binary, err := downloadLitestream(ctx, asset)
	if err != nil {
		return err
	}
	for _, path := range litestreamDirs {
		if _, recorded := h.r.Dirs[path]; recorded {
			if err := h.dir(path, 0755, 0, 0); err != nil {
				return err
			}
			continue
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			if err := h.dir(path, 0755, 0, 0); err != nil {
				return err
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0755 || info.Sys().(*syscall.Stat_t).Uid != 0 {
			return errors.New("Litestream directory ownership refused")
		}
		if err := protectedParents(path + "/check"); err != nil {
			return err
		}
	}
	if err := protectedParents(LitestreamPath); err != nil {
		return err
	}
	path := recordDir + "/litestream-stage"
	if err := h.file(ctx, path, binary, 0755, false); err != nil {
		return err
	}

	out, err := h.exec.Execute(ctx, localexec.Command{Path: path, Args: []string{"version"}, Timeout: 5 * time.Second, OutputLimit: 1024})
	if err != nil || out.Truncated || strings.TrimSpace(out.Stdout) != "0.5.17" {
		return errors.New("staged Litestream version refused")
	}
	if err := h.file(ctx, LitestreamPath, binary, 0755, false); err != nil {
		return err
	}
	return h.restoreFile(path)
}
func (h *host) undoLitestream(context.Context) error {
	if err := h.restoreFile(recordDir + "/litestream-stage"); err != nil {
		return err
	}
	if err := h.restoreFile(LitestreamPath); err != nil {
		return err
	}
	for i := len(litestreamDirs) - 1; i >= 0; i-- {
		path := litestreamDirs[i]
		old, ok := h.r.Dirs[path]
		if !ok {
			continue
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || st.Uid != 0 || st.Ino != old.Inode || info.Mode().Perm() != 0755 {
			return errors.New("undo Litestream directory drift")
		}
		if err := protectedParents(path); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return errors.New("undo Litestream tree contains unowned files")
		}
		parent, err := os.Open(filepath.Dir(path))
		if err != nil {
			return err
		}
		if err := errors.Join(parent.Sync(), parent.Close()); err != nil {
			return err
		}
	}
	return nil
}
