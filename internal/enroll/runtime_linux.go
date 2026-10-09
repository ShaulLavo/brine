package enroll

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type runtimeFile struct {
	Hash      string `json:"hash,omitempty"`
	Directory bool   `json:"directory"`
	Mode      uint32 `json:"mode"`
}

var runtimePaths = map[string]bool{
	".local/share": true, ".local/share/containers": true, ".local/share/containers/storage": true,
	".local/share/containers/storage/db.sql":                             false,
	".local/share/containers/storage/storage.lock":                       false,
	".local/share/containers/storage/userns.lock":                        false,
	".local/share/containers/storage/defaultNetworkBackend":              false,
	".local/share/containers/storage/overlay-containers":                 true,
	".local/share/containers/storage/overlay-containers/containers.lock": false,
	".local/share/containers/storage/overlay-images":                     true,
	".local/share/containers/storage/overlay-images/images.lock":         false,
	".local/share/containers/storage/overlay-layers":                     true,
	".local/share/containers/storage/overlay-layers/layers.lock":         false,
	".local/share/containers/storage/overlay":                            true,
	".local/share/containers/storage/overlay/l":                          true,
	".local/share/containers/storage/overlay/.has-mount-program":         false,
	".local/share/containers/storage/libpod":                             true,
	".local/share/containers/storage/libpod/db.sql":                      false,
	".local/share/containers/storage/networks":                           true,
	".local/share/containers/storage/networks/netavark.lock":             false,
	".local/share/containers/storage/secrets":                            true,
	".local/share/containers/storage/secrets/secrets.lock":               false,
	".local/share/containers/storage/volumes":                            true,
}

func runtimeManifest(root *os.Root, uid, gid int) (map[string]runtimeFile, error) {
	result := map[string]runtimeFile{}
	if _, err := root.Lstat(".local/share"); errors.Is(err, os.ErrNotExist) {
		return result, nil
	} else if err != nil {
		return nil, err
	}
	err := fs.WalkDir(root.FS(), ".local/share", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		directory, ok := runtimePaths[path]
		if !ok || d.IsDir() != directory || d.Type()&os.ModeSymlink != 0 {
			return errors.New("unrecorded runtime data refused")
		}
		info, err := root.Lstat(path)
		if err != nil {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		if int(st.Uid) != uid || int(st.Gid) != gid || (!directory && st.Nlink != 1) {
			return errors.New("runtime ownership or hard-link drift")
		}
		item := runtimeFile{Directory: directory, Mode: uint32(info.Mode().Perm())}
		if !directory {
			file, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if err != nil {
				return err
			}
			actual, err := file.Stat()
			if err != nil || !actual.Mode().IsRegular() || actual.Size() > 64<<20 || !os.SameFile(info, actual) {
				file.Close()
				return errors.New("runtime file type or size refused")
			}
			data, err := io.ReadAll(io.LimitReader(file, 64<<20+1))
			err = errors.Join(err, file.Close())
			if err != nil || len(data) > 64<<20 {
				return errors.New("runtime file read failed")
			}
			sum := sha256.Sum256(data)
			item.Hash = hex.EncodeToString(sum[:])
		}
		result[path] = item
		return nil
	})

	return result, err
}
func (h *host) captureRuntime(ctx context.Context) error {
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	// Verification is operator-authorized and repeats empty-resource checks before
	// refreshing hashes: even read-only Podman queries update SQLite metadata.
	// Undo never refreshes these hashes; it fails closed on subsequent drift.
	if _, err = root.Lstat(".local/share"); errors.Is(err, os.ErrNotExist) {
		h.r.Runtime = map[string]runtimeFile{}
		return h.Save(h.r.Journal)
	}
	if _, err := runtimeManifest(root, h.r.UID, h.r.GID); err != nil {
		return err
	}
	// Podman creates metadata even while listing an empty store. Only the fixed,
	// empty runtime tree is enrollment-created; app/image/volume/secret data isn't.
	for _, args := range [][]string{{"ps", "-a", "--format", "{{.ID}}"}, {"image", "ls", "--format", "{{.ID}}"}, {"volume", "ls", "--format", "{{.Name}}"}, {"secret", "ls", "--format", "{{.Name}}"}} {
		command := []string{"-u", "brine", "--", "/usr/bin/env", "XDG_CONFIG_HOME=" + home + "/.config", "XDG_DATA_HOME=" + home + "/.local/share", "XDG_CACHE_HOME=" + home + "/.cache", "XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(h.r.UID), "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/" + strconv.Itoa(h.r.UID) + "/bus", "/usr/bin/podman", "--root", home + "/.local/share/containers/storage", "--runroot", "/run/user/" + strconv.Itoa(h.r.UID) + "/containers"}
		out, err := h.run(ctx, false, "runuser", append(command, args...)...)
		if err != nil || out.Truncated || strings.TrimSpace(out.Stdout) != "" {
			return errors.New("undo provenance refused: runtime contains resources or cannot be checked")
		}
	}
	manifest, err := runtimeManifest(root, h.r.UID, h.r.GID)
	if err != nil {
		return err
	}
	h.r.Runtime = manifest
	return h.Save(h.r.Journal)
}
func (h *host) checkRuntime(root *os.Root) error {
	observed, err := runtimeManifest(root, h.r.UID, h.r.GID)
	if err != nil {
		return err
	}
	if len(observed) != len(h.r.Runtime) {
		return errors.New("undo refused: runtime manifest changed")
	}
	for p, want := range h.r.Runtime {
		if observed[p] != want {
			return errors.New("undo refused: runtime hash or metadata changed")
		}
	}
	return nil
}
func (h *host) runtimeHomeAllowed() map[string]bool {
	paths := map[string]bool{}
	for p := range h.r.Runtime {
		paths[filepath.Join(home, p)] = true
	}
	return paths
}
