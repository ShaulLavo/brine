package enroll

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeManifestRecordsOnlyEmptyMetadata(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, ".local/share/containers/storage/libpod")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(base, "db.sql")
	if err := os.WriteFile(db, []byte("fixture metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	manifest, err := runtimeManifest(root, os.Getuid(), os.Getgid())
	if err != nil || manifest[".local/share/containers/storage/libpod/db.sql"].Hash == "" {
		t.Fatalf("manifest=%v err=%v", manifest, err)
	}
	h := host{r: hostRecord{UID: os.Getuid(), GID: os.Getgid(), Runtime: manifest}}
	if err := h.checkRuntime(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("changed metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.checkRuntime(root); err == nil {
		t.Fatal("changed database was accepted")
	}
	if err := os.WriteFile(db, []byte("fixture metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(base, "application.db")
	if err := os.WriteFile(unknown, []byte("app data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeManifest(root, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("app data accepted")
	}
	if err := os.Remove(unknown); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(db); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", db); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeManifest(root, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Remove(db); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("fixture metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(db, filepath.Join(dir, "duplicate")); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeManifest(root, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("hard link accepted")
	}
}
func TestRuntimeManifestWithoutProvenanceRefusesMetadata(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	h := host{r: hostRecord{UID: os.Getuid(), GID: os.Getgid()}}
	if err := h.checkRuntime(root); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".local/share"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := h.checkRuntime(root); err == nil {
		t.Fatal("unrecorded runtime accepted")
	}
	if _, err := runtimeManifest(root, os.Getuid()+1, os.Getgid()); err == nil {
		t.Fatal("foreign owner accepted")
	}
}
