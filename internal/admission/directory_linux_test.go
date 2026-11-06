//go:build linux

package admission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func fixture(t *testing.T) (*Directory, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admission")
	if err := os.Mkdir(path, 0755); err != nil { // #nosec G301 -- Admission directories are intentionally public-readable; outer test root is private.
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0755); err != nil { // #nosec G302 -- Required admission metadata inside the private fixture root.
		t.Fatal(err)
	}
	for _, name := range []string{"state.json", "lock", "stage.lock"} {
		writeFixture(t, path, name, []byte(`{"version":1,"open":true}`))
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(fd), path)
	d, err := checkedDirectory(f, fixtureOwner(t, f))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d, path
}

func writeFixture(t *testing.T, path, name string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(path, name), raw, 0644); err != nil { // #nosec G306 -- Fixed public-readable state/lock fixture under a private root.
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(path, name), 0644); err != nil { // #nosec G302 -- Required admission metadata inside the private fixture root.
		t.Fatal(err)
	}
}

func mustClose(t *testing.T, c interface{ Close() error }) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertOpen(t *testing.T, d *Directory, open bool) State {
	t.Helper()
	s, err := d.Read(context.Background())
	if err != nil || s.Open != open {
		t.Fatalf("state: %+v, %v; want open=%v", s, err, open)
	}
	return s
}

func TestProtectedArtifacts(t *testing.T) {
	for _, name := range []string{"state.json", "lock", "stage.lock"} {
		t.Run(name, func(t *testing.T) { testBadArtifact(t, name) })
	}
}

func testBadArtifact(t *testing.T, name string) {
	for _, fault := range []string{"missing", "symlink", "hardlink", "directory", "fifo", "writable", "private", "owner"} {
		t.Run(fault, func(t *testing.T) { checkBadArtifact(t, name, fault) })
	}
}

func checkBadArtifact(t *testing.T, name, fault string) {
	d, path := fixture(t)
	target := filepath.Join(path, name)
	switch fault {
	case "missing":
		mustRemove(t, target)
	case "symlink":
		mustRemove(t, target)
		if err := os.Symlink("missing", target); err != nil {
			t.Fatal(err)
		}
	case "hardlink":
		if err := os.Link(target, target+"-link"); err != nil {
			t.Fatal(err)
		}
	case "directory":
		mustRemove(t, target)
		if err := os.Mkdir(target, 0755); err != nil { // #nosec G301 -- Admission directories are intentionally public-readable; outer test root is private.
			t.Fatal(err)
		}
	case "fifo":
		mustRemove(t, target)
		if err := unix.Mkfifo(target, 0644); err != nil {
			t.Fatal(err)
		}
	case "writable":
		if err := os.Chmod(target, 0666); err != nil { // #nosec G302 -- Deliberately unsafe metadata must be refused.
			t.Fatal(err)
		}
	case "private":
		if err := os.Chmod(target, 0600); err != nil {
			t.Fatal(err)
		}
	case "owner":
		d.owner++
	}
	f, err := d.openFile(name)
	if err == nil {
		mustClose(t, f)
		t.Fatal("unsafe file admitted")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestReadRefusesAbsentTornAndOversize(t *testing.T) {
	for _, raw := range []string{"", `{"version":1,`, strings.Repeat("x", MaxStateBytes+1)} {
		d, path := fixture(t)
		writeFixture(t, path, "state.json", []byte(raw))
		if _, err := d.Read(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%v", err)
		}
	}
}

func TestProtectedDirectoryConstructor(t *testing.T) {
	_, path := fixture(t)
	for _, name := range []string{path, "relative", path + "/../admission", "/"} {
		d, err := OpenDirectory(name)
		if err == nil {
			mustClose(t, d)
			t.Fatalf("unsafe parents accepted: %s", name)
		}
	}
	symlink := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if d, err := OpenDirectory(symlink); err == nil {
		mustClose(t, d)
		t.Fatal("symlink accepted")
	}
	for _, mode := range []os.FileMode{0700, 0775} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		// #nosec G304 -- Directory just created under the private test root.
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		d, err := checkedDirectory(f, fixtureOwner(t, f))
		if err == nil {
			mustClose(t, d)
			t.Fatal("wrong directory mode accepted")
		}
	}
}

func TestDurableWriteFaults(t *testing.T) {
	for _, fault := range []string{"file-sync", "rename", "directory-sync"} {
		t.Run(fault, func(t *testing.T) { testWriteFault(t, fault) })
	}
}

func testWriteFault(t *testing.T, fault string) {
	d, path := fixture(t)
	injected := errors.New("injected IO failure")
	d.syncFile = func(f *os.File) error {
		if fault == "file-sync" && f != d.dir || fault == "directory-sync" && f == d.dir {
			return injected
		}
		return f.Sync()
	}
	if fault == "rename" {
		d.rename = func(int, string, int, string) error { return injected }
	}
	r, err := d.CloseAdmission(context.Background(), pendingFixture())
	if r != nil || !errors.Is(err, injected) {
		t.Fatalf("%v %v", r, err)
	}
	assertOpen(t, d, fault != "directory-sync")
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("temporary leaked: %v", entries)
	}
}

func TestWriterChecksOwnerAndTarget(t *testing.T) {
	for _, fault := range []string{"wrong-writer", "symlink", "missing"} {
		d, path := fixture(t)
		changeWriterFault(t, d, path, fault)
		if err := d.save(context.Background(), State{Version: 1, Open: true}); !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	}
}

func TestChildDirectoryWalkRefusesSymlinksAndUnsafeParents(t *testing.T) {
	d, path := fixture(t)
	if err := os.Mkdir(filepath.Join(path, "child"), 0755); err != nil { // #nosec G301 -- Admission directories are intentionally public-readable; outer test root is private.
		t.Fatal(err)
	}
	if err := os.Symlink("child", filepath.Join(path, "link")); err != nil {
		t.Fatal(err)
	}
	child, err := openChildDirectory(d.dir, "child", d.owner)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, child)
	if f, err := openChildDirectory(d.dir, "link", d.owner); !errors.Is(err, ErrUnavailable) {
		if f != nil {
			mustClose(t, f)
		}
		t.Fatal(err)
	}
	if f, err := openChildDirectory(d.dir, "child", d.owner+1); !errors.Is(err, ErrUnavailable) {
		if f != nil {
			mustClose(t, f)
		}
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0777); err != nil { // #nosec G302 -- Deliberately unsafe metadata must be refused.
		t.Fatal(err)
	}
	if f, err := openChildDirectory(d.dir, "child", d.owner); !errors.Is(err, ErrUnavailable) {
		if f != nil {
			mustClose(t, f)
		}
		t.Fatal(err)
	}
	if _, err := d.Read(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func fixtureOwner(t *testing.T, f *os.File) uint32 {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	return st.Uid
}

func changeWriterFault(t *testing.T, d *Directory, path, fault string) {
	t.Helper()
	switch fault {
	case "wrong-writer":
		d.owner++
	case "symlink":
		mustRemove(t, filepath.Join(path, "state.json"))
		if err := os.Symlink("lock", filepath.Join(path, "state.json")); err != nil {
			t.Fatal(err)
		}
	case "missing":
		mustRemove(t, filepath.Join(path, "state.json"))
	}
}

func TestFileOwnerCheckIndependentlyOfDirectoryOwner(t *testing.T) {
	for _, name := range []string{"state.json", "lock", "stage.lock"} {
		d, _ := fixture(t)
		f, err := d.openFile(name)
		if err != nil {
			t.Fatal(err)
		}
		d.owner++ // Private expected-owner seam, without any privileged chown.
		err = d.checkFile(f, name)
		mustClose(t, f)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("file %s: %v", name, err)
		}
	}
}
