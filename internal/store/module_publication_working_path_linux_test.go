//go:build linux

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPublicationWorkingPathUsesPhysicalNameAndHeldIdentity(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil { //nolint:gosec // Protected fixture directory must be private and searchable.
		t.Fatal(err)
	}
	fd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot("/proc/self/fd/" + strconv.Itoa(fd))
	_ = unix.Close(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err = root.Mkdir("parent", 0700); err != nil {
		t.Fatal(err)
	}
	parent, err := root.OpenRoot("parent")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	if err = parent.Mkdir("working", 0700); err != nil {
		t.Fatal(err)
	}
	working, err := parent.OpenRoot("working")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = working.Close() }()
	identity, err := working.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	physical := filepath.Join(base, "parent", "working")
	if err = checkPublicationWorkingPath(parent, working, identity, physical); err != nil {
		t.Fatal("physical path refused held private working root", err)
	}
	if !strings.HasPrefix(parent.Name(), "/proc/self/fd/") {
		t.Fatal("fixture did not exercise descriptor-opened parent")
	}
	inferred := filepath.Join(parent.Name(), "working")
	if !errors.Is(checkPublicationWorkingPath(parent, working, identity, inferred), ErrConflict) {
		t.Fatal("descriptor spelling supplied durable pathname")
	}
	if err = parent.Mkdir("foreign", 0700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(base, "parent", "foreign")
	if !errors.Is(checkPublicationWorkingPath(parent, working, identity, foreign), ErrConflict) {
		t.Fatal("foreign named inode supplied working path")
	}
	if err = parent.Rename("working", "retained"); err != nil {
		t.Fatal(err)
	}
	if err = parent.Mkdir("working", 0700); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(checkPublicationWorkingPath(parent, working, identity, physical), ErrConflict) {
		t.Fatal("replacement directory acquired held identity")
	}
	if err = parent.Remove("working"); err != nil {
		t.Fatal(err)
	}
	if err = parent.Symlink("retained", "working"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(checkPublicationWorkingPath(parent, working, identity, physical), ErrConflict) {
		t.Fatal("symlink path acquired held identity")
	}
	retained, err := parent.OpenRoot("retained")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = retained.Close() }()
	if observed, statErr := retained.Stat("."); statErr != nil || !os.SameFile(observed, identity) {
		t.Fatal("negative checks touched retained original", statErr)
	}
}
