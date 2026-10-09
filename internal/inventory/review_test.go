//go:build linux

package inventory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
	"golang.org/x/sys/unix"
)

const unsafeConfig = `{"admin":{"listen":"0.0.0.0:2019"}}`

type warningRunner struct{}

func TestReviewAdaptWarningsCannotHideUnsafeAdmin(t *testing.T) {
	_, e := (Collector{FS: baseFixture(), Runner: warningRunner{}, IdentityKey: []byte("fixture")}).Collect(context.Background())
	var conflict *ConflictError
	if !errors.As(e, &conflict) {
		t.Fatalf("unsafe admin plus stderr warning returned %v", e)
	}
}
func TestReviewLiveAdminCheckedWhenAdaptFails(t *testing.T) {
	for _, disk := range []string{"missing", "invalid"} {
		t.Run(disk, func(t *testing.T) {
			r := fakeRunner{"uname -m": "aarch64", "curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/": unsafeConfig}
			if disk == "invalid" {
				r["caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile"] = "invalid JSON"
			}
			_, e := (Collector{FS: baseFixture(), Runner: r, IdentityKey: []byte("fixture")}).Collect(context.Background())
			var conflict *ConflictError
			if !errors.As(e, &conflict) {
				t.Fatalf("live unsafe admin not refused: %v", e)
			}
		})
	}
}
func secretOnlyFixture() fixtureFS {
	f := baseFixture()
	f.files["/etc/passwd"] = "brine:x:1001:1001::/home/brine:/bin/sh\n"
	f.files["/proc/self/status"] = "Uid:\t1001\t1001\t1001\t1001\n"
	return f
}
func TestReviewPredeploymentSecrets(t *testing.T) {
	for _, present := range []bool{true, false} {
		f := secretOnlyFixture()
		if present {
			f.dirs["/home/brine/.config/containers/systemd"] = []os.DirEntry{}
		}
		r := fakeRunner{"uname -m": "aarch64", "podman --remote=false secret ls --format {{.ID}} {{.Name}}": "fixture-id brine-api-db-v1"}
		s, e := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}).Collect(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if s.Apps.Value == nil || len(*s.Apps.Value) != 1 {
			t.Fatalf("predeployment secret lost, directory present=%v: %#v", present, s.Apps)
		}
		a := (*s.Apps.Value)[0]
		if a.Name != "api" || a.Secrets.Status != target.KnownStatus || len(*a.Secrets.Value) != 1 {
			t.Fatal("secret-only app missing")
		}
	}
}
func TestReviewFIFORejectedWithoutWriter(t *testing.T) {
	p := filepath.Join(t.TempDir(), "brine-api.container")
	if e := unix.Mkfifo(p, 0600); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := (HostFS{}).ReadFile(context.Background(), p); done <- e }()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(500 * time.Millisecond):
		fd, e := unix.Open(p, unix.O_WRONLY|unix.O_NONBLOCK, 0)
		if e == nil {
			unix.Close(fd)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("FIFO read stranded")
		}
		t.Fatal("FIFO read blocked until writer connected")
	}
}
func TestReviewOSReleaseShellEscapes(t *testing.T) {
	m, e := osRelease("ID=debian\nVERSION_ID=\"13\"\nPRETTY_NAME=\"My \\$OS and \\`literal\\`\"\n")
	if e != nil || m["ID"] != "debian" || m["PRETTY_NAME"] != "My $OS and `literal`" {
		t.Fatalf("valid shell quoting refused %#v %v", m, e)
	}
}

func (warningRunner) RunStdout(ctx context.Context, p string, args ...string) (string, error) {
	if p == "caddy" {
		return (localexec.ExecRunner{}).RunStdout(ctx, "sh", "-c", `printf '%s\n' 'Caddyfile input is not formatted' >&2; printf '%s' '{"admin":{"listen":"0.0.0.0:2019"}}'`)
	}
	return (fakeRunner{"uname -m": "aarch64"}).Run(ctx, p, args...)
}

func TestReviewFirstDeploymentBindsPreexistingSecret(t *testing.T) {
	f := secretOnlyFixture()
	r := fakeRunner{"uname -m": "aarch64", "podman --remote=false secret ls --format {{.ID}} {{.Name}}": "fixture-id brine-api-db-v1"}
	collected, e := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}).Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile("../target/testdata/ready-arm64.json")
	if e != nil {
		t.Fatal(e)
	}
	ready, e := target.Decode(data)
	if e != nil {
		t.Fatal(e)
	}
	ready.Apps = collected.Apps
	image := plan.Image{ManifestDigest: target.Observation[string]{Status: target.Unknown}, Digest: "sha256:" + strings.Repeat("a", 64), Platform: target.Platform{OS: "linux", Arch: "arm64"}}
	desired := policy.Desired{SchemaVersion: 1, Name: "api", Image: spec.ImageReference("registry.example.test/api@" + image.Digest), ContainerPort: 8080, Domains: []spec.Domain{"api.example.test"}, Environment: []policy.Environment{}, Secrets: []policy.Secret{{Name: "DB", Reference: "db"}}, PolicyVersion: "fixture", PolicyHash: "sha256:" + strings.Repeat("b", 64), AppPorts: policy.PortRange{Min: 20000, Max: 20010}}
	p, e := plan.Build(plan.Input{Desired: desired, Snapshot: ready, Image: image, State: plan.BrineState{Target: ready.Identity, Generation: 0, Releases: []plan.CurrentRelease{}}})
	if e != nil {
		t.Fatal(e)
	}
	if p.Kind != plan.Create || len(p.Secrets) != 1 || p.Secrets[0].VersionName != "brine-api-db-v1" {
		t.Fatalf("first deployment failed: %#v", p.Conflicts)
	}
}
func TestReviewAmbiguousSecretNamesRemainUnknown(t *testing.T) {
	f := secretOnlyFixture()
	r := fakeRunner{"uname -m": "aarch64", "podman --remote=false secret ls --format {{.ID}} {{.Name}}": "fixture-id brine-api-main-db-v1"}
	s, e := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}).Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if s.Apps.Status != target.Unknown {
		t.Fatal("ambiguous app/reference boundary guessed")
	}
}
func TestReviewUnreadableAdminIsUnknown(t *testing.T) {
	for _, data := range []string{"not JSON", "null", "[]", `{"admin":{"listen":42}}`} {
		if o := adminBinding([]byte(data)); o.Status != target.Unknown || o.Value != nil {
			t.Fatalf("unreadable binding called safe: %#v", o)
		}
	}
}
func TestReviewFilesystemCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	_, e := filesystemCall(ctx, func(context.Context) (string, error) { <-release; return "late", nil })
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("stalled filesystem call ignored context: %v", e)
	}
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	for _, read := range []func() error{
		func() error { _, e := (HostFS{}).ReadFile(canceled, "/etc/os-release"); return e },
		func() error { _, e := (HostFS{}).ReadDir(canceled, "/etc"); return e },
		func() error { _, e := (HostFS{}).Readlink(canceled, "/etc/os-release"); return e },
	} {
		if e := read(); !errors.Is(e, context.Canceled) {
			t.Fatalf("filesystem call ignored cancellation: %v", e)
		}
	}
}
func TestReviewFileTypeAndLimit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "regular")
	if e := os.WriteFile(p, []byte("fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "link")
	if e := os.Symlink(p, link); e != nil {
		t.Fatal(e)
	}
	b, e := (HostFS{}).ReadFile(context.Background(), link)
	if e != nil || string(b) != "fixture" {
		t.Fatalf("regular symlink failed %q %v", b, e)
	}
	if _, e = (HostFS{}).ReadFile(context.Background(), dir); e == nil {
		t.Fatal("directory accepted as file")
	}
	if _, e = (HostFS{}).ReadDir(context.Background(), p); e == nil {
		t.Fatal("regular file accepted as directory")
	}
	if e = os.WriteFile(p, make([]byte, fileLimit+1), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = (HostFS{}).ReadFile(context.Background(), p); e == nil {
		t.Fatal("oversized file accepted")
	}
}
