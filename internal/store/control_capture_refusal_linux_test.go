//go:build linux

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"
)

func TestControlCaptureRefusesUnsafeSources(t *testing.T) {
	for _, name := range []string{"directory_symlink", "parent_symlink", "db_symlink", "wal_symlink", "db_directory", "fifo", "public_directory", "public_db", "wrong_directory_owner", "orphan_wal", "oversized_db", "oversized_pair", "negative_uid", "overflow_uid"} {
		t.Run(name, func(t *testing.T) { testControlCaptureRefusal(t, name) })
	}
}

func testControlCaptureRefusal(t *testing.T, name string) {
	t.Helper()
	dir := stateDir(t)
	path := filepath.Join(dir, "control.db")
	writeCaptureFixture(t, path, []byte("fixture"))
	uid := os.Geteuid()
	switch name {
	case "directory_symlink":
		link := filepath.Join(t.TempDir(), "link")
		requireCaptureFixture(t, os.Symlink(dir, link))
		dir = link
	case "parent_symlink":
		link := filepath.Join(t.TempDir(), "link")
		requireCaptureFixture(t, os.Symlink(filepath.Dir(dir), link))
		dir = filepath.Join(link, filepath.Base(dir))
	case "db_symlink", "wal_symlink":
		plantCaptureSymlink(t, name, path)
	case "db_directory":
		requireCaptureFixture(t, os.Remove(path))
		requireCaptureFixture(t, os.Mkdir(path, 0700))
	case "fifo":
		requireCaptureFixture(t, os.Remove(path))
		requireCaptureFixture(t, unix.Mkfifo(path, 0600))
	case "public_directory":
		requireCaptureFixture(t, os.Chmod(dir, 0755)) // #nosec G302 -- Deliberately public directory tests capture refusal.
	case "public_db":
		requireCaptureFixture(t, os.Chmod(path, 0644)) // #nosec G302 -- Deliberately public database tests capture refusal.
	case "wrong_directory_owner":
		uid++
	case "orphan_wal":
		requireCaptureFixture(t, os.Remove(path))
		writeCaptureFixture(t, path+"-wal", []byte{})
	case "oversized_db":
		requireCaptureFixture(t, os.Truncate(path, maxPreviewBytes+1))
	case "oversized_pair":
		requireCaptureFixture(t, os.Truncate(path, maxPreviewBytes))
		writeCaptureFixture(t, path+"-wal", []byte("overflow"))
	case "negative_uid":
		uid = -1
	case "overflow_uid":
		uid = int(uint64(1) << 32)
	}
	if _, err := CaptureBytes(context.Background(), dir, uid); err == nil {
		t.Fatal("unsafe source accepted")
	}
}

func plantCaptureSymlink(t *testing.T, name, path string) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "target")
	writeCaptureFixture(t, target, []byte("target"))
	requireCaptureFixture(t, os.Remove(path))
	if name == "wal_symlink" {
		writeCaptureFixture(t, path, []byte("db"))
		path += "-wal"
	}
	requireCaptureFixture(t, os.Symlink(target, path))
}

func TestControlCaptureWrongFileOwner(t *testing.T) {
	dir := stateDir(t)
	writeCaptureFixture(t, filepath.Join(dir, "control.db"), []byte("fixture"))
	root, err := openControlDirectory(dir, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, _, err := readControlFile(context.Background(), root, "control.db", os.Geteuid()+1, maxPreviewBytes); !errors.Is(err, ErrPreviewUnavailable) {
		t.Fatalf("wrong owner accepted: %v", err)
	}
}

func TestControlCaptureEmptyWALPresence(t *testing.T) {
	dir := stateDir(t)
	path := filepath.Join(dir, "control.db")
	writeCaptureFixture(t, path, []byte("fixture"))
	absent, err := CaptureBytes(context.Background(), dir, os.Geteuid())
	if err != nil || absent.WAL != nil || absent.WALID != (FileID{}) {
		t.Fatalf("absent WAL: %+v %v", absent.WALID, err)
	}
	writeCaptureFixture(t, path+"-wal", []byte{})
	present, err := CaptureBytes(context.Background(), dir, os.Geteuid())
	if err != nil || present.WAL == nil || present.WALID.SHA256 != captureDigest([]byte{}) || sameControlCapture(absent, present) {
		t.Fatalf("empty WAL confused with absence: %+v %v", present.WALID, err)
	}
}

// Context checks provide a deterministic scheduling point while all mutations
// below are real filesystem changes, not replacement readers or captured bytes.
type captureBoundaryContext struct {
	context.Context
	checks atomic.Int64
	at     int
	action func()
}

func (c *captureBoundaryContext) Err() error {
	if c.checks.Add(1) == int64(c.at) {
		c.action()
	}
	return c.Context.Err()
}

func TestControlCaptureRefusesTornReads(t *testing.T) {
	for _, name := range []string{"bytes", "inode", "wal_presence", "db_absence", "directory"} {
		t.Run(name, func(t *testing.T) { testControlCaptureTear(t, name) })
	}
}

func testControlCaptureTear(t *testing.T, name string) {
	t.Helper()
	dir := stateDir(t)
	path := filepath.Join(dir, "control.db")
	writeCaptureFixture(t, path, []byte("before"))
	ctx := &captureBoundaryContext{Context: context.Background(), at: 8}
	ctx.action = func() { tearControlFixture(t, name, dir, path) }
	if _, err := CaptureBytes(ctx, dir, os.Geteuid()); !errors.Is(err, ErrPreviewUnavailable) {
		t.Fatalf("torn %s capture: %v", name, err)
	}
	if ctx.checks.Load() < int64(ctx.at) {
		t.Fatal("tear did not run")
	}
}

func tearControlFixture(t *testing.T, name, dir, path string) {
	t.Helper()
	switch name {
	case "bytes":
		writeCaptureFixture(t, path, []byte("after!"))
	case "inode":
		replacement := filepath.Join(dir, "replacement")
		writeCaptureFixture(t, replacement, []byte("before"))
		requireCaptureFixture(t, os.Rename(replacement, path))
	case "wal_presence":
		writeCaptureFixture(t, path+"-wal", []byte{})
	case "db_absence":
		requireCaptureFixture(t, os.Remove(path))
	case "directory":
		requireCaptureFixture(t, os.Rename(dir, dir+"-old"))
		t.Cleanup(func() { _ = os.RemoveAll(dir + "-old") })
		requireCaptureFixture(t, os.Mkdir(dir, 0700))
		writeCaptureFixture(t, path, []byte("before"))
	}
}

func TestControlCaptureCancellation(t *testing.T) {
	dir := stateDir(t)
	writeCaptureFixture(t, filepath.Join(dir, "control.db"), make([]byte, 256<<10))
	before := captureTree(t, dir)
	for _, boundary := range []int{1, 4} {
		testControlCaptureCancellation(t, dir, boundary)
	}
	assertCaptureTree(t, dir, before)
}

func testControlCaptureCancellation(t *testing.T, dir string, boundary int) {
	t.Helper()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &captureBoundaryContext{Context: parent, at: boundary, action: cancel}
	if _, err := CaptureBytes(ctx, dir, os.Geteuid()); !errors.Is(err, context.Canceled) {
		t.Fatalf("capture cancellation at %d: %v", boundary, err)
	}
}

func TestControlPreviewNoFollow(t *testing.T) {
	dir := stateDir(t)
	target := filepath.Join(t.TempDir(), "target")
	writeCaptureFixture(t, target, []byte("secret"))
	requireCaptureFixture(t, os.Symlink(target, filepath.Join(dir, "control.db")))
	if _, err := previewRead(context.Background(), filepath.Join(dir, "control.db")); err == nil {
		t.Fatal("preview followed symlink")
	}
}

func writeCaptureFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	requireCaptureFixture(t, os.WriteFile(path, data, 0600))
}

func requireCaptureFixture(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
