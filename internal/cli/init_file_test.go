package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/result"
	"golang.org/x/sys/unix"
)

func initFixture(t *testing.T) initPreview {
	t.Helper()
	preview, err := prepareInit(initInput{Directory: t.TempDir(), Name: "hello", Image: initArgs(".")[5], ContainerPort: 3000, Domains: []string{"hello.example.com"}, HealthPath: "/"})
	if err != nil {
		t.Fatal(err)
	}
	return preview
}

func initTestDirectory(t *testing.T, path string) *initDirectory {
	t.Helper()
	directory, err := openInitDirectory(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := directory.Close(); err != nil {
			t.Error(err)
		}
	})
	return directory
}

func TestInitPreservesFiles(t *testing.T) {
	for _, kind := range []string{"equal", "different", "symlink", "dangling", "directory", "unreadable", "fifo", "large"} {
		t.Run(kind, func(t *testing.T) { checkInitExisting(t, kind) })
	}
	t.Run("held directory", checkInitHeldDirectory)
	t.Run("replacement before equality", checkInitReplacedFile)
}

func checkInitExisting(t *testing.T, kind string) {
	t.Helper()
	preview := initFixture(t)
	path := filepath.Join(preview.Directory, initFileName)
	source := filepath.Join(preview.Directory, "source.txt")
	writeInitTestFile(t, source, []byte("existing source\n"))
	seedInitDestination(t, kind, path, source, preview.Content)
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	deps := initNoHostDependencies(t, &out, &diagnostics)
	err = Execute(deps, append(initArgs(preview.Directory), "--yes", "--json"))
	if kind == "equal" && (err != nil || !bytes.Contains(out.Bytes(), []byte(`"status":"unchanged"`))) {
		t.Fatalf("equal = %v %s", err, out.String())
	}
	if kind != "equal" && kind != "unreadable" && result.ExitCode(err) != 5 {
		t.Fatalf("conflict = %v", err)
	}
	if kind == "unreadable" && (err == nil || result.ExitCode(err) == 5) {
		t.Fatalf("unreadable = %v", err)
	}
	after, statErr := os.Lstat(path)
	if statErr != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() {
		t.Fatalf("destination changed: %v", statErr)
	}
	raw, readErr := readInitTestFile(t, preview.Directory, "source.txt")
	if readErr != nil || string(raw) != "existing source\n" {
		t.Fatal("source changed", readErr)
	}
	assertInitFiles(t, preview.Directory, []string{initFileName, "source.txt"})
}

func seedInitDestination(t *testing.T, kind, path, source string, content []byte) {
	t.Helper()
	var err error
	switch kind {
	case "equal":
		writeInitTestFile(t, path, content)
	case "different":
		writeInitTestFile(t, path, []byte("keep me"))
	case "large":
		writeInitTestFile(t, path, bytes.Repeat([]byte("x"), 2<<20))
	case "symlink":
		err = os.Symlink(source, path)
	case "dangling":
		err = os.Symlink("missing", path)
	case "directory":
		err = os.Mkdir(path, 0700)
	case "fifo":
		err = unix.Mkfifo(path, 0600)
	case "unreadable":
		writeInitTestFile(t, path, content)
		err = os.Chmod(path, 0000)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func writeInitTestFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
}

func checkInitHeldDirectory(t *testing.T) {
	t.Helper()
	parent := t.TempDir()
	selected := filepath.Join(parent, "project")
	if err := os.Mkdir(selected, 0700); err != nil {
		t.Fatal(err)
	}
	d := initTestDirectory(t, selected)
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(selected, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(selected, 0700); err != nil {
		t.Fatal(err)
	}
	status, err := d.create(t.Context(), []byte("held"), defaultInitFileOps())
	if err != nil || status != "created" {
		t.Fatal(status, err)
	}
	raw, err := readInitTestFile(t, moved, initFileName)
	if err != nil || string(raw) != "held" {
		t.Fatal("lost held directory", err)
	}
	assertInitFiles(t, selected, nil)
	assertInitFiles(t, moved, []string{initFileName})
}

func checkInitReplacedFile(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"regular", "symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) { checkInitEqualityReplacement(t, kind) })
	}
}

func checkInitEqualityReplacement(t *testing.T, kind string) {
	t.Helper()
	preview := initFixture(t)
	d := initTestDirectory(t, preview.Directory)
	path := filepath.Join(preview.Directory, initFileName)
	writeInitTestFile(t, path, preview.Content)
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	held, err := d.root.OpenFile(initFileName, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := held.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if kind == "regular" {
		writeInitTestFile(t, path, preview.Content)
	} else {
		seedInitDestination(t, kind, path, "missing", preview.Content)
	}
	if _, err := d.compare(held, before, preview.Content); result.ExitCode(err) != 5 {
		t.Fatalf("replacement accepted: %v", err)
	}
}

func TestInitFailureCleanup(t *testing.T) {
	for _, failure := range []string{"write", "sync", "close", "link", "cancel", "directory sync", "concurrent same", "concurrent different", "concurrent symlink", "concurrent fifo", "concurrent directory", "vanished race"} {
		t.Run(failure, func(t *testing.T) { checkInitFailure(t, failure) })
	}
	for _, mode := range []string{"human", "json", "jsonl", "short human", "short json", "short jsonl"} {
		t.Run("output "+mode, func(t *testing.T) { checkInitOutputFailure(t, mode) })
	}
}

var errInitFailure = errors.New("init injected failure")

func checkInitFailure(t *testing.T, failure string) {
	t.Helper()
	preview := initFixture(t)
	d := initTestDirectory(t, preview.Directory)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ops := initFailureOps(t, d, preview.Content, failure, cancel)
	status, err := d.create(ctx, preview.Content, ops)
	if failure == "concurrent same" {
		if err != nil || status != "unchanged" {
			t.Fatal(status, err)
		}
	} else if err == nil {
		t.Fatal("failure returned success", status)
	}
	published := failure == "directory sync" || failure == "concurrent same"
	if published {
		assertInitContent(t, d, preview.Content)
	}
	want := []string(nil)
	if published || failure == "concurrent different" || failure == "concurrent symlink" || failure == "concurrent fifo" || failure == "concurrent directory" {
		want = []string{initFileName}
	}
	assertInitFiles(t, preview.Directory, want)
	if failure == "cancel" && result.ExitCode(err) != 130 {
		t.Fatal(err)
	}
}

func initFailureOps(t *testing.T, d *initDirectory, content []byte, failure string, cancel context.CancelFunc) initFileOps {
	t.Helper()
	ops := defaultInitFileOps()
	switch failure {
	case "write":
		ops.write = func(*os.File, []byte) error { return errInitFailure }
	case "sync":
		ops.sync = func(*os.File) error { return errInitFailure }
	case "close":
		ops.close = func(f *os.File) error { return errors.Join(f.Close(), errInitFailure) }
	case "link":
		ops.link = func(*os.Root, string, string) error { return errInitFailure }
	case "cancel":
		ops.sync = func(f *os.File) error { cancel(); return f.Sync() }
	case "directory sync":
		ops.syncDirectory = func(*os.File) error { return errInitFailure }
	default:
		ops.link = func(root *os.Root, from, to string) error {
			return initRaceLink(t, d, content, failure, root, from, to)
		}
	}
	return ops
}

func initRaceLink(t *testing.T, d *initDirectory, content []byte, failure string, root *os.Root, from, to string) error {
	t.Helper()
	path := filepath.Join(d.root.Name(), initFileName)
	switch failure {
	case "concurrent same":
		writeInitTestFile(t, path, content)
	case "concurrent different":
		writeInitTestFile(t, path, []byte("concurrent data"))
	case "concurrent symlink":
		seedInitDestination(t, "symlink", path, "missing", content)
	case "concurrent fifo":
		seedInitDestination(t, "fifo", path, "", content)
	case "concurrent directory":
		seedInitDestination(t, "directory", path, "", content)
	case "vanished race":
		return os.ErrExist
	}
	return root.Link(from, to)
}

func assertInitContent(t *testing.T, d *initDirectory, content []byte) {
	t.Helper()
	raw, err := d.root.ReadFile(initFileName)
	if err != nil || !bytes.Equal(raw, content) {
		t.Fatal("published spec lost", err)
	}
	status, err := d.create(t.Context(), content, defaultInitFileOps())
	if err != nil || status != "unchanged" {
		t.Fatal("rerun", status, err)
	}
	info, err := d.root.Stat(initFileName)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("published permissions", info, err)
	}
}

type initFailedOutput struct{}

func (initFailedOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func checkInitOutputFailure(t *testing.T, mode string) {
	t.Helper()
	preview := initFixture(t)
	var diagnostics bytes.Buffer
	var output io.Writer = initFailedOutput{}
	if strings.HasPrefix(mode, "short ") {
		output = &finalOutputProbe{short: true}
		mode = strings.TrimPrefix(mode, "short ")
	}
	deps := initNoHostDependencies(t, output, &diagnostics)
	args := append(initArgs(preview.Directory), "--yes")
	if mode != "human" {
		args = append(args, "--"+mode)
	}
	if err := Execute(deps, args); err == nil {
		t.Fatal("failed output returned success")
	}
	assertInitContent(t, initTestDirectory(t, preview.Directory), preview.Content)
	assertInitFiles(t, preview.Directory, []string{initFileName})
	var out bytes.Buffer
	retry := initNoHostDependencies(t, &out, &diagnostics)
	if err := Execute(retry, append(initArgs(preview.Directory), "--yes", "--json")); err != nil || !bytes.Contains(out.Bytes(), []byte(`"status":"unchanged"`)) {
		t.Fatalf("rerun after output failure = %v, %s", err, out.String())
	}
}

func readInitTestFile(t *testing.T, directory, name string) ([]byte, error) {
	t.Helper()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()
	return root.ReadFile(name)
}
