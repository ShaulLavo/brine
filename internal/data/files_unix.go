//go:build linux || darwin

package data

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
func verifyPrivateFile(p string) (os.FileInfo, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) {
		return nil, ErrInvalid
	}
	return info, nil
}
func verifyPrivateTree(root, source string) error {
	if err := verifyRootAncestors(root); err != nil {
		return err
	}
	relative, err := filepath.Rel(root, source)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrInvalid
	}
	current := root
	for _, part := range append([]string{""}, strings.Split(relative, string(filepath.Separator))...) {
		if part != "" {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 || !owned(info) {
			return ErrInvalid
		}
	}
	return nil
}

func verifyRootAncestors(root string) error {
	if !ValidRoot(root) {
		return ErrInvalid
	}
	current := string(filepath.Separator)
	for _, part := range append([]string{""}, strings.Split(strings.TrimPrefix(root, current), current)...) {
		if part != "" {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) {
			return ErrInvalid
		}
		// A root-owned sticky ancestor protects owned child names from other
		// users (for example /tmp). The actual data tree remains exact 0700.
		if info.Mode().Perm()&0022 != 0 && !(stat.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return ErrInvalid
		}
	}
	return nil
}

func directoryIdentity(path string) (uint64, uint64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || !owned(info) || info.Mode().Perm() != 0700 {
		return 0, 0, ErrInvalid
	}
	return uint64(stat.Dev), uint64(stat.Ino), nil
}
