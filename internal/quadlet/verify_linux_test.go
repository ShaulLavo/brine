package quadlet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyCurrentFreshOwnedArtifactOrExplicitAbsence(t *testing.T) {
	home := t.TempDir()
	m := manager(t, home)
	defer m.Close()
	ctx := context.Background()
	previous := rendered(t, "previous")
	candidate := rendered(t, "candidate")
	if err := m.VerifyCurrent(ctx, previous.Name(), previous.Hash()); err == nil {
		t.Fatal("absence accepted as owned unit")
	}
	if err := m.VerifyCurrent(ctx, previous.Name(), previous.Hash(), ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(ctx, previous, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyCurrent(ctx, previous.Name(), candidate.Hash(), previous.Hash()); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(ctx, candidate, previous.Hash()); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyCurrent(ctx, candidate.Name(), candidate.Hash()); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyCurrent(ctx, candidate.Name(), previous.Hash()); err == nil {
		t.Fatal("wrong owned hash accepted")
	}
	path := filepath.Join(home, ActiveDirectory, candidate.Name())
	// A later independent write must invalidate the earlier probe, even if marked.
	foreign := rendered(t, "foreign")
	if err := os.WriteFile(path, foreign.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyCurrent(ctx, candidate.Name(), candidate.Hash(), previous.Hash()); err == nil {
		t.Fatal("cached ownership accepted")
	}
	if err := os.WriteFile(path, []byte("[Container]\nImage=foreign\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyCurrent(ctx, candidate.Name(), digest([]byte("[Container]\nImage=foreign\n"))); !errors.Is(err, ErrUnowned) {
		t.Fatal("unmarked file accepted", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("outside", path); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyCurrent(ctx, candidate.Name(), ""); err == nil {
		t.Fatal("unsafe file accepted as absent")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := m.VerifyCurrent(canceled, candidate.Name(), candidate.Hash()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestVerifyCurrentProbeNeverCreatesDirectories(t *testing.T) {
	home := t.TempDir()
	if err := VerifyCurrent(context.Background(), home, "hello.container", ""); err == nil {
		t.Fatal("missing parents accepted")
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("ownership probe created directories", entries, err)
	}
	m := manager(t, home)
	defer m.Close()
	u := rendered(t, "owned")
	if err := m.Install(context.Background(), u, ""); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCurrent(context.Background(), home, u.Name(), u.Hash()); err != nil {
		t.Fatal(err)
	}
	// Ownership reads must not go through the mutation durability hook.
	m.syncDir = func(string) error { t.Fatal("read-only probe synced a directory"); return nil }
	if err := m.VerifyCurrent(context.Background(), u.Name(), u.Hash()); err != nil {
		t.Fatal(err)
	}
}
