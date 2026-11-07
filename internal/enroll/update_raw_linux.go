//go:build linux

package enroll

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"

	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

type rawRollback struct {
	CaptureDigest string          `json:"capture_digest"`
	Directory     admissionNodeID `json:"directory"`
	Quarantine    admissionNodeID `json:"quarantine"`
	Moves         [3]*rawMove     `json:"moves"`
	Install       [2]*rawInstall  `json:"install"`
}
type rawMove struct {
	Original UpdateFile `json:"original"`
	Moved    bool       `json:"moved"`
}
type rawInstall struct {
	File UpdateFile `json:"file"`
	Done bool       `json:"done"`
}

var rawNames = [3]string{"control.db-shm", "control.db-wal", "control.db"}

// restoreUpdateCapture is deliberately private and journal-bound. It accepts
// neither a source path nor an installation path from a command or request.
func (h *host) restoreUpdateCapture(ctx context.Context, capture store.Capture) (err error) {
	u := h.r.Update
	if !u.Aborting || u.Capture == nil || captureDigest(capture) != u.Capture.Digest {
		return errors.New("rollback_capture_mismatch")
	}
	if err := h.updateProof(ctx); err != nil {
		return err
	}
	dir, err := h.updateEnv.fs.directory("/"+admissionState, h.r.UID)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	identity := updateNodeIdentity(info)
	if u.Raw == nil {
		var st unix.Stat_t
		err := unix.Fstatat(int(dir.Fd()), "quarantine-"+u.ID, &st, unix.AT_SYMLINK_NOFOLLOW)
		if !errors.Is(err, unix.ENOENT) {
			return errors.Join(errors.New("quarantine_drift"), err)
		}
		u.Raw = &rawRollback{CaptureDigest: u.Capture.Digest, Directory: identity}
		if err := h.updateSave(); err != nil {
			return err
		}
	}
	if u.Raw.Directory != identity || u.Raw.CaptureDigest != captureDigest(capture) {
		return errors.New("rollback_directory_drift")
	}
	q, err := h.rawQuarantine(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	for index := range rawNames {
		if err := h.rawMove(ctx, dir, q, index); err != nil {
			return err
		}
	}
	if err := h.updateProof(ctx); err != nil {
		return err
	}
	for index, data := range [][]byte{capture.DB, capture.WAL} {
		if err := h.rawInstall(ctx, dir, index, data); err != nil {
			return err
		}
	}
	return h.verifyRawRestore(ctx, capture)
}
func (h *host) rawQuarantine(dir *os.File) (*os.File, error) {
	raw := h.r.Update.Raw
	name := "quarantine-" + h.r.Update.ID
	if raw.Quarantine == (admissionNodeID{}) {
		err := unix.Mkdirat(int(dir.Fd()), name, 0700)
		if err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
	}
	fd, err := unix.Openat2(int(dir.Fd()), name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return nil, err
	}
	q := os.NewFile(uintptr(fd), name)
	if err := h.bindQuarantine(dir, q); err != nil {
		return nil, errors.Join(err, q.Close())
	}
	return q, nil
}
func (h *host) bindQuarantine(dir, q *os.File) error {
	raw := h.r.Update.Raw
	info, err := q.Stat()
	if err != nil {
		return err
	}
	identity := updateNodeIdentity(info)
	if raw.Quarantine != (admissionNodeID{}) {
		if raw.Quarantine != identity {
			return errors.New("quarantine_drift")
		}
		return nil
	}
	names, err := q.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) != 0 {
		return errors.New("quarantine_drift")
	}
	if err := q.Chown(h.r.UID, h.r.GID); err != nil {
		return err
	}
	if err := q.Chmod(0700); err != nil {
		return err
	}
	if err := errors.Join(q.Sync(), dir.Sync()); err != nil {
		return err
	}
	raw.Quarantine = identity
	return h.updateSave()
}
func rawRead(dir *os.File, name string, uid int) (observation UpdateFile, err error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return UpdateFile{}, nil
	}
	if err != nil {
		return observation, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { err = errors.Join(err, f.Close()) }()
	observation, _, err = updateObservation(f)
	if err != nil || observation.UID != uid || observation.Mode&0077 != 0 {
		return observation, errors.Join(errors.New("rollback_file_drift"), err)
	}
	return observation, nil
}
func (h *host) rawMove(ctx context.Context, dir, q *os.File, index int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := rawNames[index]
	source, err := rawRead(dir, name, h.r.UID)
	if err != nil {
		return err
	}
	target, err := rawRead(q, name, h.r.UID)
	if err != nil {
		return err
	}
	move := h.r.Update.Raw.Moves[index]
	if move == nil {
		if target.Present {
			return errors.New("quarantine_drift")
		}
		move = &rawMove{Original: source}
		h.r.Update.Raw.Moves[index] = move
		if err := h.updateSave(); err != nil {
			return err
		}
	}
	if move.Moved {
		return h.rawMoved(source, target, move, index)
	}
	if !move.Original.Present {
		if source.Present || target.Present {
			return errors.New("quarantine_drift")
		}
		move.Moved = true
		return h.updateSave()
	}
	if updateSame(target, move.Original) && !source.Present {
		move.Moved = true
		return h.updateSave()
	}
	if !updateSame(source, move.Original) || target.Present {
		return errors.New("quarantine_lost_or_drift")
	}
	if err := unix.Renameat(int(dir.Fd()), name, int(q.Fd()), name); err != nil {
		return err
	}
	if err := errors.Join(dir.Sync(), q.Sync()); err != nil {
		return err
	}
	return h.rawMove(ctx, dir, q, index)
}
func (h *host) rawMoved(source, target UpdateFile, move *rawMove, index int) error {
	if !updateSame(target, move.Original) {
		return errors.New("quarantine_drift")
	}
	if !source.Present {
		return nil
	}
	installIndex := 2 - index
	if installIndex < 0 || installIndex >= 2 {
		return errors.New("quarantine_drift")
	}
	install := h.r.Update.Raw.Install[installIndex]
	if install == nil || !updateSame(source, install.File) {
		return errors.New("quarantine_drift")
	}
	return nil
}
func (h *host) rawInstall(ctx context.Context, dir *os.File, index int, data []byte) error {
	name := []string{"control.db", "control.db-wal"}[index]
	tmp := "." + name + ".update-" + h.r.Update.ID
	current, err := rawRead(dir, name, h.r.UID)
	if err != nil {
		return err
	}
	intent := h.r.Update.Raw.Install[index]
	if data == nil {
		if current.Present {
			return errors.New("rollback_absence_drift")
		}
		return nil
	}
	if intent != nil && updateSame(current, intent.File) {
		intent.Done = true
		return h.updateSave()
	}
	if current.Present {
		return errors.New("rollback_install_drift")
	}
	if intent == nil {
		file, err := h.rawStage(dir, tmp, data)
		if err != nil {
			return err
		}
		intent = &rawInstall{File: file}
		h.r.Update.Raw.Install[index] = intent
		if err := h.updateSave(); err != nil {
			return err
		}
	}
	staged, err := rawRead(dir, tmp, h.r.UID)
	if err != nil || !updateSame(staged, intent.File) {
		return errors.Join(errors.New("rollback_install_lost"), err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Renameat(int(dir.Fd()), tmp, int(dir.Fd()), name); err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		return err
	}
	return h.rawInstall(ctx, dir, index, data)
}
func (h *host) rawStage(dir *os.File, name string, data []byte) (UpdateFile, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if errors.Is(err, unix.EEXIST) {
		observed, e := rawRead(dir, name, h.r.UID)
		if e != nil || observed.SHA256 != hash(data) {
			return UpdateFile{}, errors.Join(errors.New("rollback_staging_drift"), e)
		}
		return observed, nil
	}
	if err != nil {
		return UpdateFile{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	_, err = file.Write(data)
	if err == nil {
		err = file.Chown(h.r.UID, h.r.GID)
	}
	err = errors.Join(err, file.Sync(), file.Close(), dir.Sync())
	if err != nil {
		return UpdateFile{}, err
	}
	return rawRead(dir, name, h.r.UID)
}
func (h *host) verifyRawRestore(ctx context.Context, want store.Capture) error {
	if _, err := os.Lstat(h.updateEnv.fs.path("/" + admissionState + "/control.db-shm")); !errors.Is(err, os.ErrNotExist) {
		return errors.Join(errors.New("rollback_shm_drift"), err)
	}
	got, err := store.CaptureBytes(ctx, h.updateEnv.fs.path("/"+admissionState), h.r.UID)
	if err != nil {
		return err
	}
	if got.Absent != want.Absent || !bytes.Equal(got.DB, want.DB) || !bytes.Equal(got.WAL, want.WAL) || (got.WAL == nil) != (want.WAL == nil) {
		return errors.New("rollback_capture_mismatch")
	}
	facts, err := store.InspectCapture(ctx, got, h.updateEnv.fs.path(updateStagePath(h.r.Update.ID)))
	if err != nil || facts.Version != h.r.Update.Plan.SchemaFrom || facts.Absent != want.Absent || !facts.Absent && (!facts.Integrity || !facts.ForeignKeys) {
		return errors.Join(errors.New("rollback_schema_mismatch"), err)
	}
	return nil
}
