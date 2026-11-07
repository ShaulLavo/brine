//go:build linux

package enroll

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type updateFS struct {
	root  string
	owner int
}

func (f updateFS) path(path string) string {
	return filepath.Join(f.root, strings.TrimPrefix(path, "/"))
}

func (f updateFS) directory(path string, owner int) (*os.File, error) {
	if owner == f.owner {
		if err := f.protectedUpdateParents(path); err != nil {
			return nil, err
		}
	}
	file, err := proofOpen(f.path(path), unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && (int(info.Sys().(*syscall.Stat_t).Uid) != owner || info.Mode().Perm()&0022 != 0) {
		err = errors.New("update directory ownership drift")
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func updateObservation(file *os.File) (UpdateFile, []byte, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return UpdateFile{}, nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Size > 64<<20 {
		return UpdateFile{}, nil, errors.New("update file type or size refused")
	}
	data, err := io.ReadAll(io.LimitReader(file, 64<<20+1))
	if err != nil || len(data) > 64<<20 {
		return UpdateFile{}, nil, errors.Join(errors.New("update read refused"), err)
	}
	var after unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &after); err != nil {
		return UpdateFile{}, nil, err
	}
	if st.Dev != after.Dev || st.Ino != after.Ino || st.Size != after.Size || st.Mode != after.Mode || st.Uid != after.Uid || st.Gid != after.Gid || st.Nlink != after.Nlink || st.Mtim != after.Mtim || st.Ctim != after.Ctim {
		return UpdateFile{}, nil, errors.New("update file changed during read")
	}
	return UpdateFile{Present: true, SHA256: hash(data), Dev: uint64(st.Dev), Ino: st.Ino, Size: st.Size, Mode: st.Mode & 07777, UID: int(st.Uid), GID: int(st.Gid)}, data, nil
}

func (f updateFS) read(path string) (observation UpdateFile, data []byte, err error) {
	if err := f.protectedUpdateParents(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return observation, nil, nil
		}
		return observation, nil, err
	}
	file, err := proofOpen(f.path(path), unix.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return UpdateFile{}, nil, nil
	}
	if err != nil {
		return observation, nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return updateObservation(file)
}

func (f updateFS) mkdir(path string, mode os.FileMode, owner, group int) (err error) {
	parent, err := f.directory(filepath.Dir(path), f.owner)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	name := filepath.Base(path)
	err = unix.Mkdirat(int(parent.Fd()), name, uint32(mode))
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	child, err := proofOpen(f.path(path), unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, child.Close()) }()
	var st unix.Stat_t
	if err := unix.Fstat(int(child.Fd()), &st); err != nil {
		return err
	}
	if int(st.Uid) != owner || int(st.Gid) != group || st.Mode&07777 != uint32(mode) {
		return errors.New("update staging directory drift")
	}
	return errors.Join(child.Sync(), parent.Sync())
}

func (f updateFS) write(path string, data []byte, mode uint32, uid, gid int) (observed UpdateFile, err error) {
	parent, err := f.directory(filepath.Dir(path), f.owner)
	if err != nil {
		return UpdateFile{}, err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	id, err := updateID()
	if err != nil {
		return UpdateFile{}, err
	}
	name := ".brine-update-" + id
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return UpdateFile{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { err = errors.Join(err, removeUpdateTemporary(int(parent.Fd()), name)) }()
	if _, err = file.Write(data); err == nil {
		err = file.Chmod(os.FileMode(mode))
	}
	if err == nil && (uid != os.Geteuid() || gid != os.Getegid()) {
		err = file.Chown(uid, gid)
	}
	err = errors.Join(err, file.Sync(), file.Close())
	if err != nil {
		return UpdateFile{}, err
	}
	if err := unix.Renameat(int(parent.Fd()), name, int(parent.Fd()), filepath.Base(path)); err != nil {
		return UpdateFile{}, err
	}
	if err := parent.Sync(); err != nil {
		return UpdateFile{}, err
	}
	observed, _, err = f.read(path)
	expected := UpdateFile{Present: true, SHA256: hash(data), Size: int64(len(data)), Mode: mode, UID: uid, GID: gid}
	if err == nil && !updateEquivalent(observed, expected) {
		err = errors.New("update write readback drift")
	}
	return observed, err
}

func updateEquivalent(a, b UpdateFile) bool {
	return a.Present == b.Present && a.SHA256 == b.SHA256 && a.Size == b.Size && a.Mode == b.Mode && a.UID == b.UID && a.GID == b.GID
}

func updateSame(a, b UpdateFile) bool {
	return updateEquivalent(a, b) && a.Dev == b.Dev && a.Ino == b.Ino
}

func (f updateFS) remove(path string) (err error) {
	parent, err := f.directory(filepath.Dir(path), f.owner)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	err = unix.Unlinkat(int(parent.Fd()), filepath.Base(path), 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return errors.Join(err, parent.Sync())
}

func stageArtifact(id string, index int, side string) string {
	return recordDir + "/update/" + id + "/" + side + "-" + strconv.Itoa(index)
}
func updateStagePath(id string) string    { return recordDir + "/update/" + id }
func updateError(code, path string) error { return fmt.Errorf("%s: %s", code, path) }

func removeUpdateTemporary(fd int, name string) error {
	err := unix.Unlinkat(fd, name, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

func (f updateFS) protectedUpdateParents(path string) error {
	for parent := filepath.Dir(f.path(path)); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || int(st.Uid) != f.owner || info.Mode().Perm()&0022 != 0 {
			return errors.New("update parent ownership drift")
		}
		if parent == f.root {
			return nil
		}
		if parent == "/" {
			return errors.New("update path outside injected root")
		}
	}
}
