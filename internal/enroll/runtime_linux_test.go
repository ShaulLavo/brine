package enroll

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/localexec"
	"os"
	"path/filepath"
	"strings"
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

func TestEmptyRuntimeChecksEveryResourceWithRunnerEnvironment(t *testing.T) {
	cases := []struct {
		name        string
		output      localexec.Result
		err         error
		wantFailure bool
	}{
		{name: "empty"},
		{name: "resource", output: localexec.Result{Stdout: "fixture-resource\n"}, wantFailure: true},
		{name: "truncated", output: localexec.Result{Truncated: true}, wantFailure: true},
		{name: "unknown", err: errors.New("probe failed"), wantFailure: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
				argv := strings.Join(c.Args, " ")
				if c.Path != "/usr/sbin/runuser" || c.Mutation || !strings.Contains(argv, "-u brine -- /usr/bin/env XDG_CONFIG_HOME=/home/brine/.config") || !strings.Contains(argv, "XDG_RUNTIME_DIR=/run/user/1234") || !strings.Contains(argv, "--root /home/brine/.local/share/containers/storage --runroot /run/user/1234/containers") {
					t.Fatalf("unsafe runtime command: %+v", c)
				}
				return tt.output, tt.err
			}}
			h := host{exec: f, r: hostRecord{UID: 1234}}
			err := h.checkEmptyRuntime(context.Background())
			if (err != nil) != tt.wantFailure {
				t.Fatalf("error=%v", err)
			}
			if !tt.wantFailure && len(f.commands) != 4 {
				t.Fatal("did not check containers, images, volumes and secrets")
			}
		})
	}
}
