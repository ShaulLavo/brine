//go:build linux

package runnerstorage

import (
	"bytes"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func CheckLocal() error {
	return checkLocal(Path, FusePath, "/dev/fuse")
}

func protectedRegular(path string, mode os.FileMode, max int64) (*os.File, error) {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return nil, ErrProfile
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || stat.Gid != 0 {
			return nil, ErrProfile
		}
		if parent == "/" {
			break
		}
	}
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0) //nolint:gosec // Fixed protected profile/helper paths; parents and opened metadata are checked.
	if err != nil {
		return nil, ErrProfile
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, ErrProfile
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode() != mode || info.Size() <= 0 || info.Size() > max || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 {
		_ = file.Close()
		return nil, ErrProfile
	}
	return file, nil
}

func checkLocal(config, helper, device string) error {
	file, err := protectedRegular(config, 0644, 4096)
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(raw, []byte(Config)) {
		return ErrProfile
	}
	executable, err := protectedRegular(helper, 0755, 8<<20)
	if err != nil {
		return err
	}
	defer func() { _ = executable.Close() }()
	var caps [128]byte
	if _, err = unix.Fgetxattr(int(executable.Fd()), "security.capability", caps[:]); err == nil || !errors.Is(err, unix.ENODATA) {
		return ErrProfile
	}
	info, err := os.Lstat(device)
	if err != nil || !validFuseDevice(info) || unix.Access(device, unix.R_OK|unix.W_OK) != nil {
		return ErrProfile
	}
	return nil
}

func validFuseDevice(info os.FileInfo) bool {
	if info.Mode()&os.ModeCharDevice == 0 || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0 && stat.Gid == 0 && unix.Major(uint64(stat.Rdev)) == 10 && unix.Minor(uint64(stat.Rdev)) == 229
}
