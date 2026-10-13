//go:build linux

package runnerstorage

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStorageProfileRefusesMissingDriftAndUnprotectedOwnership(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "profile")
	if err := checkLocal(path, "/usr/bin/true", "/dev/null"); err == nil {
		t.Fatal("missing profile admitted")
	}
	for _, raw := range []string{Config, Config + "\n", "[storage]\ndriver=\"vfs\"\n"} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		if err := checkLocal(path, "/usr/bin/true", "/dev/null"); err == nil {
			t.Fatal("unprotected profile admitted")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", path); err != nil {
		t.Fatal(err)
	}
	if _, err := protectedRegular(path, 0644, 4096); err == nil {
		t.Fatal("symlink profile admitted")
	}
}

type deviceFixture struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (d deviceFixture) Sys() any { return &d.stat }
func TestStorageDeviceRequiresActualFUSEKernelDevice(t *testing.T) {
	info, err := os.Lstat("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	if validFuseDevice(info) {
		t.Fatal("unrelated root-owned character device admitted")
	}
	fixture := deviceFixture{FileInfo: info, stat: *info.Sys().(*syscall.Stat_t)}
	fixture.stat.Rdev = uint64(unix.Mkdev(10, 229))
	if !validFuseDevice(fixture) {
		t.Fatal("exact FUSE device refused")
	}
	fixture.stat.Uid = 1000
	if validFuseDevice(fixture) {
		t.Fatal("caller-owned FUSE device admitted")
	}
}
