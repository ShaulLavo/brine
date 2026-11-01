//go:build linux

package enroll

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUDPGrantFenceSharesMutationLockAndRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mutation.lock")
	lock, err := lockUDPGrantState(t.Context(), path, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if second, err := lockUDPGrantState(ctx, path, os.Getuid(), os.Getgid()); err == nil {
		_ = second.Close()
		t.Fatal("operator fence did not share existing mutation lock")
	}
	link := filepath.Join(dir, "linked.lock")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if held, err := lockUDPGrantState(t.Context(), link, os.Getuid(), os.Getgid()); err == nil {
		_ = held.Close()
		t.Fatal("operator lock followed symlink")
	}
}
