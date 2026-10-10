package diagnose

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/target"
)

type routeFixtureFS struct{ root string }

func (f routeFixtureFS) ReadFile(_ context.Context, path string) ([]byte, error) {
	return os.ReadFile(filepath.Join(f.root, strings.TrimPrefix(path, "/")))
}
func (f routeFixtureFS) ReadDir(_ context.Context, path string) ([]fs.DirEntry, error) {
	return os.ReadDir(filepath.Join(f.root, strings.TrimPrefix(path, "/")))
}
func (f routeFixtureFS) Readlink(_ context.Context, path string) (string, error) {
	return os.Readlink(filepath.Join(f.root, strings.TrimPrefix(path, "/")))
}

type routeFixtureRunner map[string]string

func (r routeFixtureRunner) RunStdout(_ context.Context, path string, args ...string) (string, error) {
	out, ok := r[strings.Join(append([]string{path}, args...), " ")]
	if !ok {
		return "", fs.ErrNotExist
	}
	return out, nil
}

func collectedRouting(t *testing.T) (inventory.Collector, routeFixtureFS, routeFixtureRunner) {
	t.Helper()
	f := routeFixtureFS{root: t.TempDir()}
	write := func(path, content string) {
		file := filepath.Join(f.root, strings.TrimPrefix(path, "/"))
		if e := os.MkdirAll(filepath.Dir(file), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(file, []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("/etc/os-release", "ID=debian\nVERSION_ID=13\n")
	write("/etc/machine-id", strings.Repeat("1", 32))
	write("/etc/ssh/ssh_host_ed25519_key.pub", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA fixture")
	write("/etc/passwd", "root:x:0:0::/root:/bin/sh\n")
	write("/etc/caddy/Caddyfile", "{\n local_certs\n}\nimport /etc/caddy/brine/current/*.caddy\n")
	write("/etc/caddy/brine/gen-1/hello.caddy", "hello.example.com {\n\treverse_proxy 127.0.0.1:20000\n\theader X-Content-Type-Options nosniff\n\theader Referrer-Policy no-referrer\n}\n")
	if e := os.Symlink("gen-1", filepath.Join(f.root, "etc/caddy/brine/current")); e != nil {
		t.Fatal(e)
	}
	read := func(path string) string {
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	whole := read("../inventory/testdata/import-global.json")
	r := routeFixtureRunner{
		"uname -m": "aarch64",
		"caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile":                         whole,
		"caddy adapt --config /etc/caddy/brine/current/hello.caddy --adapter caddyfile":         read("../inventory/testdata/import-site.json"),
		"curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/": whole,
	}
	return inventory.Collector{FS: f, Runner: r, IdentityKey: []byte("fixture-only-key")}, f, r
}

func TestLiveGenerationFromProductionCollector(t *testing.T) {
	collector, _, _ := collectedRouting(t)
	report, e := (Reader{Inventory: collector}).Read(context.Background(), Request{App: "hello"})
	if e != nil {
		t.Fatal(e)
	}
	if report.Host.LiveGeneration.Status != "known" || report.Host.LiveGeneration.Value == nil || *report.Host.LiveGeneration.Value != 1 {
		t.Fatalf("healthy generation unproven: %+v", report.Host.LiveGeneration)
	}
	if report.Apps[0].RoutePresent.Value == nil || !*report.Apps[0].RoutePresent.Value {
		t.Fatalf("healthy route not found: %+v", report.Apps[0].RoutePresent)
	}
}

func TestLiveGenerationRequiresEveryCollectedSource(t *testing.T) {
	collector, _, _ := collectedRouting(t)
	for _, test := range []struct {
		name string
		edit func(*target.Snapshot)
	}{
		{"missing file", func(s *target.Snapshot) {
			s.CaddyConfig.Value.Files = append(s.CaddyConfig.Value.Files, target.CaddyFile{Name: "missing.caddy", Hash: "sha256:" + strings.Repeat("a", 64)})
		}},
		{"wrong app", func(s *target.Snapshot) {
			for i := range *s.LiveCaddyFiles.Value {
				if (*s.LiveCaddyFiles.Value)[i].Name == "hello.caddy" {
					(*s.LiveCaddyFiles.Value)[i].App = "other"
				}
			}
		}},
		{"unknown domains", func(s *target.Snapshot) {
			for i := range *s.LiveCaddyFiles.Value {
				if (*s.LiveCaddyFiles.Value)[i].Name == "hello.caddy" {
					(*s.LiveCaddyFiles.Value)[i].Domains = target.Observation[[]string]{Status: target.Unknown}
				}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, e := collector.Collect(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			test.edit(&s)
			got := liveGeneration(s)
			if got.Status != "unknown" || got.Reason != "live_generation_unproven" {
				t.Fatalf("unproven source accepted: %+v", got)
			}
		})
	}
}

func (r routeFixtureRunner) CaptureStdout(ctx context.Context, limit int, path string, args ...string) (localexec.Capture, error) {
	out, err := r.RunStdout(ctx, path, args...)
	capture := localexec.Capture{Stdout: out, Overflow: len(out) > limit}
	if capture.Overflow {
		capture.Stdout = out[:limit]
	}
	return capture, err
}
