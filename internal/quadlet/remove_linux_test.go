package quadlet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
)

func TestRemoveOwnedUnitOnlyAndTwice(t *testing.T) {
	home := t.TempDir()
	m := manager(t, home)
	defer m.Close()
	ctx := context.Background()
	u := rendered(t, "value")
	if err := m.Install(ctx, u, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ActiveDirectory, u.Name())
	if err := m.Remove(ctx, u.Name(), "sha256:"+strings.Repeat("e", 64)); !errors.Is(err, ErrDrift) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := m.Remove(ctx, u.Name(), u.Hash()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	data := []byte("[Container]\nImage=example\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(ctx, u.Name(), digest(data)); !errors.Is(err, ErrUnowned) {
		t.Fatalf("unmarked: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("outside", path); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(ctx, u.Name(), u.Hash()); !errors.Is(err, ErrUnowned) {
		t.Fatalf("symlink: %v", err)
	}
}
func TestRemoveMountedDataRefusesArchive(t *testing.T) {
	home := t.TempDir()
	m := manager(t, home)
	defer m.Close()
	u := rendered(t, "value")
	path := filepath.Join(home, ActiveDirectory, u.Name())
	data := append(u.Bytes(), []byte("Volume=/srv/app:/data\n")...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyRemove(context.Background(), u.Name(), digest(data)); !errors.Is(err, plan.ErrPersistentData) {
		t.Fatal(err)
	}
	if err := m.Remove(context.Background(), u.Name(), digest(data)); !errors.Is(err, plan.ErrPersistentData) {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(data) {
		t.Fatal("changed persistent unit", err)
	}
}
