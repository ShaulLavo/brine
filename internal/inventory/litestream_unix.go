//go:build linux || darwin

package inventory

import (
	"os"
	"syscall"
)

func protectedTool(info os.FileInfo, dir bool) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return false
	}
	if dir {
		return info.IsDir() && info.Mode().Perm()&0022 == 0
	}
	return info.Mode().IsRegular() && info.Mode().Perm() == 0755 && st.Nlink == 1
}
func openTool(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
