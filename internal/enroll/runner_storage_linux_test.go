//go:build linux

package enroll

import (
	"context"
	"github.com/ShaulLavo/brine/internal/runnerstorage"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestEnrollmentRequiresReviewedFUSEPackage(t *testing.T) {
	f := supported()
	delete(f.Packages, "fuse-overlayfs")
	f.PackageInstall = []Package{{Name: "fuse-overlayfs", Version: "1.14-1+b1"}}
	p, err := MakePlan(f)
	if err != nil || !slices.Contains(p.MissingPackages, "fuse-overlayfs") {
		t.Fatal("missing helper lost exact package preview", err)
	}
	f.PackageInstall = nil
	if _, err := MakePlan(f); err == nil {
		t.Fatal("missing helper without reviewed install admitted")
	}
	for _, version := range []string{"1.15-1", "1.14.1-1", "caller"} {
		f.Packages["fuse-overlayfs"] = version
		if _, err := MakePlan(f); err == nil {
			t.Fatal("unqualified helper package admitted")
		}
	}
}
func TestRunnerProfileStepPrecedesRootlessVerification(t *testing.T) {
	h := host{}
	steps := h.steps()
	profile, layout, verify := -1, -1, -1
	for i, s := range steps {
		switch s.Name {
		case "layout":
			layout = i
		case "runner-storage":
			profile = i
		case "boot-reconcile":
			verify = i
		}
	}
	if profile <= layout || verify <= profile {
		t.Fatal("rootless verification precedes protected profile", layout, profile, verify)
	}
	if ok, err := h.checkRunnerStorage(context.Background()); err != nil || ok {
		t.Fatal("unrecorded profile adopted")
	}
	h.r.Files = map[string]ownedFile{runnerstorage.Path: {Hash: "changed", Mode: 0644}}
	if _, err := h.checkRunnerStorage(context.Background()); err == nil {
		t.Fatal("changed canonical profile adopted")
	}
}

func TestRunnerStorageDirectoryUndoConvergesAfterRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	owned := ownedDir{Inode: stat.Ino, Mode: 0700, UID: int(stat.Uid), GID: int(stat.Gid)}
	h := host{}
	if err := h.removeRunnerStorageDirectory(path, owned); err != nil {
		t.Fatal(err)
	}
	if err := h.removeRunnerStorageDirectory(path, owned); err != nil {
		t.Fatal("crash-after-removal retry failed", err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	owned.Inode = 0
	owned.Pending = true
	if err := h.removeRunnerStorageDirectory(path, owned); err != nil {
		t.Fatal("pending owned directory leaked", err)
	}
}
