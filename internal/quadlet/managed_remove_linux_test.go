//go:build linux

package quadlet

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedFrontendRemovalRetainsExactBytesAndRefusesForeignRestoration(t *testing.T) {
	home := t.TempDir()
	m := manager(t, home)
	defer m.Close()
	ctx := context.Background()
	unit := rendered(t, "value")
	if err := m.Install(ctx, unit, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveRetained(ctx, unit.Name(), unit.Hash()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ActiveDirectory, unit.Name())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("frontend not removed", err)
	}
	foreign := rendered(t, "foreign")
	if err := os.WriteFile(path, foreign.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.RestoreRemoved(ctx, unit.Name(), unit.Hash()); err == nil {
		t.Fatal("foreign active bytes replaced")
	}
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, foreign.Bytes()) {
		t.Fatal("foreign bytes changed", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := m.RestoreRemoved(ctx, unit.Name(), unit.Hash()); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, unit.Bytes()) {
		t.Fatal("retained bytes not restored", err)
	}
}
