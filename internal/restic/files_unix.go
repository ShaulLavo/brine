//go:build linux || darwin

package restic

import (
	"os"
	"path/filepath"
	"syscall"
)

func privateDirectory(directory string) error {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return ErrInvalid
	}
	for p := directory; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() {
			return ErrInvalid
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) {
			return ErrInvalid
		}
		if info.Mode().Perm()&0022 != 0 && !(stat.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return ErrInvalid
		}
		if p == directory && (stat.Uid != uint32(os.Geteuid()) || info.Mode() != os.ModeDir|0700) {
			return ErrInvalid
		}
		if p == "/" {
			return nil
		}
	}
}

func safeNode(info os.FileInfo, rootInfo os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	rootStat, rootOK := rootInfo.Sys().(*syscall.Stat_t)
	if !ok || !rootOK || stat.Uid != uint32(os.Geteuid()) || stat.Dev != rootStat.Dev {
		return false
	}
	if info.IsDir() {
		return info.Mode() == os.ModeDir|0700
	}
	return info.Mode() == 0600 && stat.Nlink == 1
}

func protectedNode(info os.FileInfo, executable bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 {
		return false
	}
	if executable {
		return info.Mode() == 0755 && stat.Nlink == 1
	}
	return info.IsDir()
}

func openMember(root *os.Root, member string) (*os.File, error) {
	return root.OpenFile(member, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

func openArchive(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
