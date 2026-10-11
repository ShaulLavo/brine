package quadlet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
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
	for _, directive := range []string{"Volume=/srv/app:/data", "Volume = /srv/app:/data", "Mount = type=bind,source=/srv/app,destination=/data", "ReadWritePaths = /srv/app", "\tVolume\t = /srv/app:/data"} {
		t.Run(directive, func(t *testing.T) {
			home := t.TempDir()
			m := manager(t, home)
			defer m.Close()
			u := rendered(t, "value")
			path := filepath.Join(home, ActiveDirectory, u.Name())
			data := append(u.Bytes(), []byte(directive+"\n")...)
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
		})
	}
}

func TestArchiveRemovalAcceptsRenderedPersistentUnitAndPreservesData(t *testing.T) {
	home := t.TempDir()
	m := manager(t, home)
	defer func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	}()
	d, p := persistentFixture(t)
	dataRoot := t.TempDir()
	d.Databases[0].PersistentRoot = data.PersistentRoot(dataRoot)
	p.DesiredHash = bind(t, d).DesiredHash
	p.DataMounts[0].Database.Root = data.PersistentRoot(dataRoot)
	p.DataMounts[0].HostPath = filepath.Join(dataRoot, p.DataMounts[0].Database.RelativeDirectory)
	if err := os.MkdirAll(p.DataMounts[0].HostPath, 0700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(p.DataMounts[0].HostPath, "app.db")
	if err := os.WriteFile(databasePath, []byte("retained database"), 0600); err != nil {
		t.Fatal(err)
	}
	u, err := Render(d, p, manifest())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(u.Bytes()), "Volume=") {
		t.Fatal("not a persistent Quadlet")
	}
	if err = m.Install(context.Background(), u, ""); err != nil {
		t.Fatal(err)
	}
	if err = m.VerifyRemove(context.Background(), u.Name(), u.Hash()); !errors.Is(err, plan.ErrPersistentData) {
		t.Fatal(err)
	}
	if err = m.VerifyArchiveRemove(context.Background(), u.Name(), u.Hash()); err != nil {
		t.Fatal(err)
	}
	if err = m.RemoveArchived(context.Background(), u.Name(), u.Hash()); err != nil {
		t.Fatal(err)
	}
	if err = m.VerifyArchiveRemove(context.Background(), u.Name(), u.Hash()); err == nil {
		t.Fatal("absent unit mistaken for pristine runtime")
	}
	// #nosec G304 -- Fixed filename inside this test's privately created database directory.
	if got, err := os.ReadFile(databasePath); err != nil || string(got) != "retained database" {
		t.Fatal("deleted database", err)
	}
}

func TestArchiveRemovalInspectionDoesNotAdoptDriftOrChangeArtifacts(t *testing.T) {
	home := t.TempDir()
	m := manager(t, home)
	defer func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	}()
	u := rendered(t, "owned")
	ctx := context.Background()
	if present, err := m.InspectArchiveRemove(ctx, u.Name(), u.Hash()); err != nil || present {
		t.Fatal("absence not distinguished", present, err)
	}
	if err := m.Install(ctx, u, ""); err != nil {
		t.Fatal(err)
	}
	if present, err := m.InspectArchiveRemove(ctx, u.Name(), u.Hash()); err != nil || !present {
		t.Fatal("presence not distinguished", present, err)
	}
	file := filepath.Join(home, ActiveDirectory, u.Name())
	changed := append(u.Bytes(), []byte("# changed\n")...)
	if err := os.WriteFile(file, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InspectArchiveRemove(ctx, u.Name(), u.Hash()); err == nil {
		t.Fatal("drift adopted")
	}
	// #nosec G304 -- Rendered unit filename inside this test's private home.
	raw, err := os.ReadFile(file)
	if err != nil || string(raw) != string(changed) {
		t.Fatal("inspection changed artifact", err)
	}
}

func TestArchiveReadOnlyInspectionNeverCreatesMissingRuntimeDirectories(t *testing.T) {
	home := t.TempDir()
	u := rendered(t, "owned")
	if _, err := InspectArchiveRemove(context.Background(), home, u.Name(), u.Hash()); err == nil {
		t.Fatal("missing runtime namespace adopted")
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("read-only inspection created runtime directories", entries, err)
	}
	m := manager(t, home)
	if err = m.Install(context.Background(), u, ""); err != nil {
		t.Fatal(err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	if present, err := InspectArchiveRemove(context.Background(), home, u.Name(), u.Hash()); err != nil || !present {
		t.Fatal("owned runtime was not inspected", present, err)
	}
	if err = os.Remove(filepath.Join(home, stagingDirectory)); err != nil {
		t.Fatal(err)
	}
	if _, err = InspectArchiveRemove(context.Background(), home, u.Name(), u.Hash()); err == nil {
		t.Fatal("missing staging namespace adopted")
	}
	if _, err = os.Stat(filepath.Join(home, stagingDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing staging namespace recreated", err)
	}
}
