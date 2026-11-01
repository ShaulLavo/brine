//go:build linux

package enroll

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/store"
)

func TestUDPGrantAdmissionInitialAndUnknownLayouts(t *testing.T) {
	for _, layout := range []string{"empty", "mutation", "missing", "wal", "shm", "unknown", "launch", "nonempty-lock", "symlink-lock", "public-dir"} {
		t.Run(layout, func(t *testing.T) { checkUDPGrantAdmissionLayout(t, layout) })
	}
}

func checkUDPGrantAdmissionLayout(t *testing.T, layout string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil { // #nosec G302 -- Test-owned runner directory requires private owner traversal.
		t.Fatal(err)
	}
	path := dir
	if layout == "missing" {
		path = filepath.Join(dir, "absent")
	}
	prepareUDPAdmissionLayout(t, dir, layout)
	held, err := store.ReadUDPGrantHeld(t.Context(), path, "hello")
	initial := layout == "empty" || layout == "mutation"
	if held || initial && err != nil || !initial && err == nil {
		t.Fatal("unknown state must not mean no reservation", held, err)
	}
	if _, err := os.Lstat(filepath.Join(path, "control.db")); !os.IsNotExist(err) {
		t.Fatal("admission created or repaired main database", err)
	}
}

func prepareUDPAdmissionLayout(t *testing.T, dir, layout string) {
	t.Helper()
	if layout == "public-dir" {
		if err := os.Chmod(dir, 0755); err != nil { // #nosec G302 -- Deliberately public test-owned directory must be refused.
			t.Fatal(err)
		}
		return
	}
	if layout == "symlink-lock" {
		if err := os.Symlink("missing", filepath.Join(dir, "mutation.lock")); err != nil {
			t.Fatal(err)
		}
		return
	}
	name := map[string]string{"mutation": "mutation.lock", "wal": "control.db-wal", "shm": "control.db-shm", "unknown": "unexpected", "launch": "launch.lock", "nonempty-lock": "mutation.lock"}[layout]
	if name == "" {
		return
	}
	var data []byte
	if layout != "mutation" {
		data = []byte("unexplained state")
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}
