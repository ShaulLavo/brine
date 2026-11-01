//go:build linux

package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type udpAdmissionFile struct {
	Bytes []byte
	Mode  os.FileMode
	Mtime time.Time
}

func udpAdmissionFiles(t *testing.T, root string) map[string]udpAdmissionFile {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]udpAdmissionFile{".": udpAdmissionReadFile(t, root, false)}
	for _, entry := range entries {
		out[entry.Name()] = udpAdmissionReadFile(t, filepath.Join(root, entry.Name()), true)
	}
	return out
}

func udpAdmissionReadFile(t *testing.T, path string, read bool) udpAdmissionFile {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	value := udpAdmissionFile{Mode: info.Mode(), Mtime: info.ModTime()}
	if read {
		value.Bytes, err = os.ReadFile(path) // #nosec G304 -- Enumerated files belong to this test's private control-store fixture.
	}
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestUDPSnapshotAdmissionPreservesLiveFiles(t *testing.T) {
	for _, wal := range []bool{false, true} {
		t.Run(map[bool]string{false: "checkpointed", true: "committed-wal"}[wal], func(t *testing.T) {
			checkUDPSnapshotAdmission(t, wal)
		})
	}
}

func checkUDPSnapshotAdmission(t *testing.T, wal bool) {
	t.Helper()
	dir := stateDir(t)
	writer, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := writer.AcquireLaunchLock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = launch.Release(); err != nil {
		t.Fatal(err)
	}
	fence, err := writer.AcquireHostLock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Release() })
	prepareUDPSnapshotWriter(t, writer, wal)
	before := udpAdmissionFiles(t, dir)
	reader, err := OpenSnapshotReadOnly(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if reader != nil {
			_ = reader.Close()
		}
	}()
	held, readErr := reader.UDPGrantHeld(t.Context(), "hello")
	admitted, admissionErr := ReadUDPGrantHeld(t.Context(), dir, "hello")
	if admissionErr != nil || admitted != wal {
		t.Fatal("operator admission must see committed reservations", admitted, admissionErr)
	}
	if during := udpAdmissionFiles(t, dir); !reflect.DeepEqual(before, during) {
		t.Fatal("admission changed live files while reading")
	}
	closeErr := reader.Close()
	reader = nil
	if readErr != nil || closeErr != nil || held != wal {
		t.Fatal("snapshot must read committed WAL reservations", held, readErr, closeErr)
	}
	if after := udpAdmissionFiles(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatal("admission changed live file set, bytes, permissions or mtimes")
	}
}

func prepareUDPSnapshotWriter(t *testing.T, writer *Store, wal bool) {
	t.Helper()
	if !wal {
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Cleanup(func() { _ = writer.Close() })
	op := operation(t, writer)
	if _, err := writer.db.Exec("PRAGMA wal_autocheckpoint=0; PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if err := insertUDPFixture(t, writer, "udp-admission", "hello", op, 4433); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(writer.dir, "control.db-wal"))
	if err != nil || info.Size() <= 32 {
		t.Fatal("fixture needs a committed WAL", err)
	}
}
