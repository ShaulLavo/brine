//go:build linux

package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type updateRetiredStage struct {
	ID        string                `json:"id"`
	Directory admissionNodeID       `json:"directory"`
	Files     map[string]UpdateFile `json:"files"`
}

func (h *host) pruneRecordedUpdates(ctx context.Context) error {
	u := h.r.Update
	if err := h.pruneRecordedUpdate(ctx, u.PreviousAborted); err != nil {
		return err
	}
	if u.Aborting {
		return nil
	}
	return h.pruneRecordedUpdate(ctx, u.PreviousUpdate)
}
func (h *host) pruneRecordedUpdate(ctx context.Context, id string) (err error) {
	u := h.r.Update
	if id == "" || id == u.ID {
		return nil
	}
	if !proofID(id) {
		return errors.New("retained_update_drift")
	}
	path := updateStagePath(id)
	fs := h.updateEnv.fs
	dir, err := fs.directory(path, fs.owner)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	if u.Retired == nil || u.Retired.ID != id {
		if err := h.previousRetirementComplete(id); err != nil {
			return err
		}
		retired, err := h.retiredUpdateManifest(dir, id, updateNodeIdentity(info))
		if err != nil {
			return err
		}
		u.Retired = retired
		if err := h.updateSave(); err != nil {
			return err
		}
	}
	if u.Retired.ID != id || u.Retired.Directory != updateNodeIdentity(info) {
		return errors.New("retained_update_drift")
	}
	for name, expected := range u.Retired.Files {
		if err := h.removeRetiredUpdateFile(ctx, path, name, expected); err != nil {
			return err
		}
	}
	parent, err := fs.directory(recordDir+"/update", fs.owner)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	if err := unix.Unlinkat(int(parent.Fd()), id, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return parent.Sync()
}
func (h *host) retiredUpdateManifest(dir *os.File, id string, identity admissionNodeID) (*updateRetiredStage, error) {
	fs := h.updateEnv.fs
	path := updateStagePath(id)
	_, data, err := fs.read(path + "/manifest.json")
	if err != nil {
		return nil, err
	}
	var plan UpdatePlan
	if err := json.Unmarshal(data, &plan); err != nil || plan.EnrollmentID != h.r.ID || plan.Digest != updateDigest(plan) {
		return nil, errors.New("retained_update_drift")
	}
	names, err := dir.Readdirnames(128)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(names) == 128 {
		return nil, errors.New("retained_update_drift")
	}
	retired := &updateRetiredStage{ID: id, Directory: identity, Files: map[string]UpdateFile{}}
	for _, name := range names {
		if !retainedUpdateName(name) {
			return nil, errors.New("retained_update_drift")
		}
		file, _, err := fs.read(filepath.Join(path, name))
		if err != nil || file.UID != fs.owner || file.Mode != 0600 {
			return nil, errors.Join(errors.New("retained_update_drift"), err)
		}
		retired.Files[name] = file
	}
	return retired, nil
}
func retainedUpdateName(name string) bool {
	if name == "manifest.json" || name == "updater" || name == "capture.db" || name == "capture.wal" {
		return true
	}
	for _, prefix := range []string{"before-", "after-"} {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		index, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
		return err == nil && index >= 0 && index < int(artifactCount)
	}
	return false
}
func (h *host) removeRetiredUpdateFile(ctx context.Context, path, name string, expected UpdateFile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	actual, _, err := h.updateEnv.fs.read(filepath.Join(path, name))
	if err != nil {
		return err
	}
	if !actual.Present {
		return nil
	}
	if !updateSame(actual, expected) {
		return errors.New("retained_update_drift")
	}
	return h.updateEnv.fs.remove(filepath.Join(path, name))
}

func (h *host) cleanupUpdateTemporaries() error {
	parents := map[string]bool{updateStagePath(h.r.Update.ID): true}
	for _, item := range h.r.Update.Plan.Artifacts {
		if item.Class == string(artifactUpdatable) {
			parents[filepath.Dir(item.Path)] = true
		}
	}
	for parent := range parents {
		if err := h.cleanupUpdateTemporaryDirectory(parent); err != nil {
			return err
		}
	}
	return nil
}
func (h *host) cleanupUpdateTemporaryDirectory(parent string) (err error) {
	fs := h.updateEnv.fs
	dir, err := fs.directory(parent, fs.owner)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	names, err := dir.Readdirnames(2048)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) == 2048 {
		return errors.New("update temporary directory limit")
	}
	for _, name := range names {
		if !strings.HasPrefix(name, ".brine-write-") && !strings.HasPrefix(name, ".brine-update-") {
			continue
		}
		if err := h.removeUpdateTemporary(parent, name); err != nil {
			return err
		}
	}
	return nil
}
func (h *host) removeUpdateTemporary(parent, name string) error {
	fs := h.updateEnv.fs
	file, _, err := fs.read(filepath.Join(parent, name))
	if err != nil {
		return err
	}
	if !file.Present || file.UID != fs.owner || file.Mode&0022 != 0 {
		return errors.New("update temporary ownership drift")
	}
	return fs.remove(filepath.Join(parent, name))
}

func (h *host) previousRetirementComplete(id string) error {
	old := h.r.Update.Retired
	if old == nil || old.ID == id {
		return nil
	}
	if _, err := os.Lstat(h.updateEnv.fs.path(updateStagePath(old.ID))); !errors.Is(err, os.ErrNotExist) {
		return errors.Join(errors.New("retained_update_drift"), err)
	}
	return nil
}
