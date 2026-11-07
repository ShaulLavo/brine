//go:build linux

package enroll

import (
	"context"
	"errors"
	"io"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func (h *host) retireEnrollmentUpdates(ctx context.Context) error {
	if h.r.Update != nil || h.r.Journal.Phase != Removed {
		return errors.New("enrollment removal incomplete")
	}
	if h.r.UndoUpdate != nil && h.r.UndoUpdate.ID != h.r.LastUpdate && h.r.UndoUpdate.ID != h.r.AbortedUpdate {
		return errors.New("retained_update_drift")
	}
	if h.updateEnv == nil {
		h.updateEnv = h.productionUpdateEnvironment()
	}
	for _, id := range []string{h.r.AbortedUpdate, h.r.LastUpdate} {
		if id == "" {
			continue
		}
		if err := h.retireEnrollmentUpdate(ctx, id); err != nil {
			return err
		}
	}
	return h.removeUpdateDirectory()
}

func (h *host) retireEnrollmentUpdate(ctx context.Context, id string) error {
	if err := h.retireUpdateStage(ctx, id, &h.r.UndoUpdate); err != nil {
		return err
	}
	if h.r.LastUpdate == id {
		h.r.LastUpdate = ""
	}
	if h.r.AbortedUpdate == id {
		h.r.AbortedUpdate = ""
	}
	h.r.UndoUpdate = nil
	return h.updateSave()
}

func (h *host) removeUpdateDirectory() (err error) {
	fs := h.updateEnv.fs
	parent, err := fs.directory(recordDir, fs.owner)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	err = unix.Unlinkat(int(parent.Fd()), "update", unix.AT_REMOVEDIR)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return errors.Join(err, parent.Sync())
}

func (h *host) removeEnrollmentRecord(ctx context.Context) (err error) {
	if err := h.retireEnrollmentUpdates(ctx); err != nil {
		return err
	}
	fs := h.updateEnv.fs
	dir, err := fs.directory(recordDir, fs.owner)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	names, err := dir.Readdirnames(3)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	for _, name := range names {
		if name != "record.json" && name != "lock" {
			return errors.New("enrollment record directory contains unowned data")
		}
	}
	if err := fs.remove(recordDir + "/record.json"); err != nil {
		return err
	}
	if err := fs.remove(recordDir + "/lock"); err != nil {
		return err
	}
	parent, err := fs.directory(filepath.Dir(recordDir), fs.owner)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	if err := unix.Unlinkat(int(parent.Fd()), filepath.Base(recordDir), unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return parent.Sync()
}
