package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/localexec"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

type fixtureFS struct {
	files    map[string]string
	dirs     map[string][]fs.DirEntry
	links    map[string]string
	failures map[string]error
}

func (f fixtureFS) ReadFile(_ context.Context, p string) ([]byte, error) {
	if e := f.failures[p]; e != nil {
		return nil, e
	}
	s, ok := f.files[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(s), nil
}
func (f fixtureFS) ReadDir(_ context.Context, p string) ([]fs.DirEntry, error) {
	if e := f.failures[p]; e != nil {
		return nil, e
	}
	v, ok := f.dirs[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return v, nil
}
func (f fixtureFS) Readlink(_ context.Context, p string) (string, error) {
	v, ok := f.links[p]
	if !ok {
		return "", fs.ErrNotExist
	}
	return v, nil
}

type fakeRunner map[string]string

func (r fakeRunner) Run(_ context.Context, p string, args ...string) (string, error) {
	s, ok := r[strings.Join(append([]string{p}, args...), " ")]
	if !ok {
		return "", fs.ErrNotExist
	}
	return s, nil
}
func baseFixture() fixtureFS {
	return fixtureFS{files: map[string]string{"/etc/os-release": "ID=debian\nVERSION_ID=\"13\"\n", "/etc/machine-id": strings.Repeat("1", 32), "/etc/ssh/ssh_host_ed25519_key.pub": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA fixture", "/etc/passwd": "root:x:0:0::/root:/bin/sh\n", "/sys/fs/cgroup/cgroup.controllers": "cpu memory"}, dirs: map[string][]fs.DirEntry{"/home": {}, "/": {}}, links: map[string]string{}, failures: map[string]error{}}
}
func TestFreshHost(t *testing.T) {
	f := baseFixture()
	r := fakeRunner{"uname -m": "aarch64\n", "ss -H -ltnpe": "", "ss -H -lunp": "", "df -B1 --output=avail /home": "Avail\n42\n"}
	s, e := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture-only-key")}).Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Validate(); e != nil {
		t.Fatal(e)
	}
	if s.Arch != "arm64" || s.Generation.Value == nil || *s.Generation.Value != 0 || s.Versions.Podman.Status != target.Absent || s.Runner.User.Status != target.Absent {
		t.Fatalf("unexpected observations %#v", s)
	}
	if s.FreeDiskBytes.Value == nil || *s.FreeDiskBytes.Value != 42 {
		t.Fatal("disk not measured")
	}
	if strings.Contains(s.Identity.ID, strings.Repeat("1", 32)) {
		t.Fatal("machine id leaked")
	}
	encoded, e := target.Encode(s)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = target.Decode(encoded); e != nil {
		t.Fatal(e)
	}
}
func TestRequiredFactsFailClosed(t *testing.T) {
	for _, p := range []string{"/etc/machine-id", "/etc/os-release", "/etc/ssh/ssh_host_ed25519_key.pub"} {
		t.Run(p, func(t *testing.T) {
			f := baseFixture()
			delete(f.files, p)
			_, e := (Collector{FS: f, Runner: fakeRunner{"uname -m": "aarch64"}, IdentityKey: []byte("key")}).Collect(context.Background())
			if e == nil {
				t.Fatal("missing required fact accepted")
			}
		})
	}
}
func TestUnknownNotZero(t *testing.T) {
	f := baseFixture()
	f.failures["/etc/passwd"] = fs.ErrPermission
	f.failures["/sys/fs/cgroup/cgroup.controllers"] = fs.ErrPermission
	s, e := (Collector{FS: f, Runner: fakeRunner{"uname -m": "x86_64"}, IdentityKey: []byte("key")}).Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if s.Runner.User.Status != target.Unknown || s.CgroupV2.Status != target.Unknown || s.UsedPorts.Status != target.Unknown || s.FreeDiskBytes.Status != target.Unknown {
		t.Fatal("failed probe fabricated facts")
	}
}
func TestUnsupportedOSPreserved(t *testing.T) {
	f := baseFixture()
	f.files["/etc/os-release"] = "ID=arch\nVERSION_ID=rolling\n"
	s, e := (Collector{FS: f, Runner: fakeRunner{"uname -m": "x86_64"}, IdentityKey: []byte("key")}).Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	var u *target.UnsupportedError
	if !errors.As(s.Validate(), &u) {
		t.Fatal("unsupported OS accepted")
	}
}
func TestPiFixtures(t *testing.T) {
	data, e := os.ReadFile("testdata/pi-probes.json")
	if e != nil {
		t.Fatal(e)
	}
	var r fakeRunner
	if e = json.Unmarshal(data, &r); e != nil {
		t.Fatal(e)
	}
	f := baseFixture()
	for path, fixture := range map[string]string{"/proc/1142/cgroup": "testdata/pi-ssh-cgroup", "/etc/os-release": "testdata/pi-os-release"} {
		data, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		f.files[path] = string(data)
	}
	s, e := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}).Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Validate(); e != nil {
		t.Fatal(e)
	}
	if s.PortOwners.Value == nil || len(*s.PortOwners.Value) == 0 {
		t.Fatal("port owners missing")
	}
	found := false
	for _, o := range *s.PortOwners.Value {
		if o.Port == 22 && o.Unit == "ssh.service" {
			found = true
		}
	}
	if !found {
		t.Fatal("PID cgroup unit missing")
	}
}

type fixtureEntry string

func (e fixtureEntry) Name() string               { return string(e) }
func (e fixtureEntry) IsDir() bool                { return false }
func (e fixtureEntry) Type() fs.FileMode          { return 0 }
func (e fixtureEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrInvalid }

func TestRoutingAndPortsWithOwners(t *testing.T) {
	f := baseFixture()
	f.links["/etc/caddy/brine/current"] = "gen-7"
	f.dirs["/etc/caddy/brine/current"] = []fs.DirEntry{fixtureEntry("api.caddy"), fixtureEntry("extra.caddy")}
	f.files["/etc/caddy/brine/current/api.caddy"] = "api.example.test { respond ok }\n"
	f.files["/etc/caddy/brine/current/extra.caddy"] = "extra.example.test { respond ok }\n"
	f.files["/proc/99/cgroup"] = "0::/system.slice/unrelated.service\n"
	f.files["/etc/caddy/Caddyfile"] = "api.example.test { respond ok }\n"
	jsonConfig := `{"apps":{"http":{"servers":{"srv0":{"routes":[{"match":[{"host":["API.EXAMPLE.TEST","*.example.test"]}],"handle":[{"handler":"static_response","body":"ok"}]}]}}}}}`
	r := fakeRunner{"uname -m": "aarch64", "ss -H -ltnpe": `LISTEN 0 4096 [::]:20001 [::]:* users:(("unrelated",pid=99,fd=3))`, "ss -H -lunp": "UNCONN 0 0 *:20002 *:*", "caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile": jsonConfig, "curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/": jsonConfig}
	s, e := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}).Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if s.CaddyConfig.Value == nil || s.CaddyConfig.Value.Generation != 7 || len(s.CaddyConfig.Value.Files) != 2 {
		t.Fatal("generation hashes missing")
	}
	if s.CaddyConfig.Value.Files[0].Hash != digest([]byte(f.files["/etc/caddy/brine/current/api.caddy"])) {
		t.Fatal("hash differs from exact bytes")
	}
	if s.LiveCaddyFiles.Value == nil || len(*s.LiveCaddyFiles.Value) != 1 {
		t.Fatal("served domains missing")
	}
	if d := (*s.LiveCaddyFiles.Value)[0].Domains; d.Value == nil || strings.Join(*d.Value, ",") != "*.example.test,api.example.test" {
		t.Fatalf("unexpected domains %#v", d)
	}
	if p := s.PortOwners.Value; p == nil || len(*p) != 1 || (*p)[0].Unit != "unrelated.service" || (*p)[0].App != "" {
		t.Fatal("invented app ownership")
	}
	if len(*s.UsedPorts.Value) != 2 {
		t.Fatal("UDP reservation missing")
	}
}

func TestOwnedQuadletsAndSecretNames(t *testing.T) {
	f := baseFixture()
	f.files["/etc/passwd"] = "brine:x:1001:1001::/home/brine:/bin/sh\n"
	f.files["/proc/self/status"] = "Uid:\t1001\t1001\t1001\t1001\n"
	f.files["/var/lib/systemd/linger/brine"] = ""
	dir := "/home/brine/.config/containers/systemd"
	f.dirs[dir] = []fs.DirEntry{fixtureEntry("brine-api.container"), fixtureEntry("brine-api.volume"), fixtureEntry("other.container")}
	f.files[dir+"/brine-api.container"] = "[Container]\nImage=example.test/api:latest\nSecret=brine-api-db-v1\n"
	f.files[dir+"/brine-api.volume"] = "[Volume]\n"
	f.files[dir+"/other.container"] = "[Container]\nImage=example.test/other:latest\n"
	r := fakeRunner{"uname -m": "x86_64", "podman --remote=false secret ls --format {{.ID}} {{.Name}}": "fixture-id brine-api-db-v1\nunrelated-id unrelated\n"}
	s, e := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}).Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if !*s.Runner.Linger.Value {
		t.Fatal("linger not measured")
	}
	if s.Apps.Value == nil || len(*s.Apps.Value) != 1 {
		t.Fatal("incorrect owned app set")
	}
	a := (*s.Apps.Value)[0]
	if len(*a.QuadletUnits.Value) != 2 || a.Image.Status != target.Unknown || a.AllocatedHostPort.Status != target.Unknown {
		t.Fatal("unmeasured app values guessed")
	}
	if a.Secrets.Value == nil || len(*a.Secrets.Value) != 1 || (*a.Secrets.Value)[0].Name != "brine-api-db-v1" {
		t.Fatal("secret names missing")
	}
}

func TestPresentStateRequiresReader(t *testing.T) {
	f := baseFixture()
	f.files["/etc/passwd"] = "brine:x:1001:1001::/home/brine:/bin/sh\n"
	f.dirs["/home/brine/.local/state/brine"] = []fs.DirEntry{}
	c := Collector{FS: f, Runner: fakeRunner{"uname -m": "aarch64"}, IdentityKey: []byte("fixture")}
	s, e := c.Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if s.Generation.Status != target.Unknown {
		t.Fatal("present state treated as generation zero")
	}
	c.StateGeneration = func(context.Context) (uint64, error) { return 19, nil }
	s, e = c.Collect(context.Background())
	if e != nil || s.Generation.Value == nil || *s.Generation.Value != 19 {
		t.Fatal("state reader ignored")
	}
}

func TestProbeFailures(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		err          error
		want         target.Status
	}{{"absent", "", fs.ErrNotExist, target.Absent}, {"path absent", "", exec.ErrNotFound, target.Absent}, {"permission", "", fs.ErrPermission, target.Unknown}, {"timeout", "", context.DeadlineExceeded, target.Unknown}, {"truncated", strings.Repeat("1", 4096), nil, target.Unknown}, {"malformed", "not a version", nil, target.Unknown}, {"present", "2.6.2", nil, target.KnownStatus}} {
		t.Run(tc.name, func(t *testing.T) {
			c := Collector{Runner: probeRunner{tc.output, tc.err}}
			if o := c.version(context.Background(), "caddy", []string{"version"}); o.Status != tc.want {
				t.Fatalf("got %s want %s", o.Status, tc.want)
			}
		})
	}
}

type probeRunner struct {
	out string
	err error
}

func (r probeRunner) Run(ctx context.Context, _ string, _ ...string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		panic("probe missing deadline")
	}
	return r.out, r.err
}

func TestCaddyUnknownAndCatchAll(t *testing.T) {
	for _, tc := range []struct {
		data    string
		ok      bool
		domains string
	}{{`{"apps":{"http":{"servers":{"a":{"routes":[{"handle":[{"handler":"file_server"}]}]}}}}}`, true, "*"}, {`{"apps":{"http":{"servers":{"a":{"routes":[{"match":[{"path":["/health"]}]}]}}}}}`, true, "*"}, {`{"apps":{"http":{"servers":{"a":{"routes":[{"match":[{"host":["user:secret@example.test"]}]}]}}}}}`, false, ""}, {`truncated`, false, ""}} {
		d, ok := caddyDomains([]byte(tc.data))
		if ok != tc.ok || strings.Join(d, ",") != tc.domains {
			t.Fatalf("got %#v %v", d, ok)
		}
	}
}

func TestIdentityStableAndKeyed(t *testing.T) {
	c := Collector{FS: baseFixture(), Runner: fakeRunner{"uname -m": "aarch64"}, IdentityKey: []byte("first")}
	a, e := c.Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	b, e := c.Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if a.Identity != b.Identity {
		t.Fatal("identity unstable")
	}
	c.IdentityKey = []byte("second")
	b, e = c.Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if a.Identity.ID == b.Identity.ID {
		t.Fatal("identity not keyed")
	}
}

func TestCapturedSnapshotStrictlyDecodes(t *testing.T) {
	data, e := os.ReadFile("testdata/pi-snapshot.json")
	if e != nil {
		t.Fatal(e)
	}
	s, e := target.Decode(data)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Validate(); e != nil {
		t.Fatal(e)
	}
	if s.OS.ID != "debian" || s.OS.Version != "13" || s.Arch != "arm64" {
		t.Fatal("wrong target evidence")
	}
	if s.Runner.User.Status != target.Absent || s.LiveCaddyFiles.Status != target.Unknown {
		t.Fatal("inactive runtime fabricated")
	}
}
func TestCaddyImportSourcesAndUnknownRoot(t *testing.T) {
	f := baseFixture()
	f.files["/etc/caddy/Caddyfile"] = "import /etc/caddy/brine/current/*.caddy\n"
	f.files["/etc/caddy/brine/current/api.caddy"] = "api.example.test { respond ok }\n"
	f.dirs["/etc/caddy/brine/current"] = []fs.DirEntry{fixtureEntry("api.caddy")}
	cfg := `{"apps":{"http":{"servers":{"srv":{"routes":[{"match":[{"host":["api.example.test"]}]}]}}}}}`
	r := fakeRunner{"uname -m": "aarch64", "caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile": cfg, "caddy adapt --config /etc/caddy/brine/current/api.caddy --adapter caddyfile": cfg, "curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/": cfg}
	s, e := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}).Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if s.LiveCaddyFiles.Value == nil || len(*s.LiveCaddyFiles.Value) != 2 {
		t.Fatal("import source omitted")
	}
	known, unknown := 0, 0
	for _, file := range *s.LiveCaddyFiles.Value {
		switch file.Domains.Status {
		case target.KnownStatus:
			known++
		case target.Unknown:
			unknown++
		}
	}
	if known != 1 || unknown != 1 {
		t.Fatal("source provenance guessed")
	}
	f.files["/etc/caddy/Caddyfile"] = "import dynamic-snippet\n"
	s, e = (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}).Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if s.LiveCaddyFiles.Status != target.Unknown {
		t.Fatal("unsupported import treated as complete")
	}
}
func TestCaddyRuntimeMismatch(t *testing.T) {
	f := baseFixture()
	f.files["/etc/caddy/Caddyfile"] = "api.example.test { respond ok }"
	r := fakeRunner{"uname -m": "aarch64", "caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile": `{"apps":{"http":{"servers":{"srv":{"routes":[{"match":[{"host":["api.example.test"]}]}]}}}}}`, "curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/": `{"apps":{"http":{"servers":{"srv":{"routes":[{"match":[{"host":["old.example.test"]}]}]}}}}}`}
	s, e := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}).Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if s.LiveCaddyFiles.Status != target.Unknown {
		t.Fatal("disk mistaken for live config")
	}
}

func TestOffLoopbackAdminRefused(t *testing.T) {
	r := fakeRunner{"uname -m": "aarch64", "caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile": `{"admin":{"listen":"0.0.0.0:2019"}}`}
	_, e := (Collector{FS: baseFixture(), Runner: r, IdentityKey: []byte("fixture")}).Collect(context.Background())
	var conflict *ConflictError
	if !errors.As(e, &conflict) {
		t.Fatal("off-loopback admin accepted")
	}
}

func TestSecretRecordsNeverReadValues(t *testing.T) {
	for _, tc := range []struct {
		out   string
		ok    bool
		count int
	}{{"", true, 0}, {"fixture-id brine-api-db-v1\n", true, 1}, {"fixture-id brine-api-db-v1 secret-value", false, 0}, {"id name\nid other", false, 0}} {
		s, ok := secretRecords(tc.out)
		if ok != tc.ok || len(s) != tc.count {
			t.Fatalf("unexpected secret records %#v %v", s, ok)
		}
	}
}

func TestRuntimeRequiresFullConfigMatch(t *testing.T) {
	if sameJSON([]byte(`{"apps":{},"admin":{}}`), []byte(`{ "admin": {}, "apps": {} }`)) != true {
		t.Fatal("JSON formatting mistaken for drift")
	}
	if sameJSON([]byte(`{"apps":{},"admin":{"disabled":false}}`), []byte(`{"apps":{},"admin":{"disabled":true}}`)) {
		t.Fatal("matching host claims hide runtime drift")
	}
}

func TestMalformedRunnerRecordIsUnknown(t *testing.T) {
	f := baseFixture()
	f.files["/etc/passwd"] = "brine:x:1001:1001:bad\n"
	s, e := (Collector{FS: f, Runner: fakeRunner{"uname -m": "aarch64"}, IdentityKey: []byte("fixture")}).Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if s.Runner.User.Status != target.Unknown || s.Generation.Status != target.Unknown || s.Apps.Status != target.Unknown {
		t.Fatal("malformed runner treated as absent")
	}
}

func (r fakeRunner) RunStdout(ctx context.Context, p string, args ...string) (string, error) {
	return r.Run(ctx, p, args...)
}
func (r probeRunner) RunStdout(ctx context.Context, p string, args ...string) (string, error) {
	return r.Run(ctx, p, args...)
}

func TestRealRunnerMissingToolIsAbsent(t *testing.T) {
	c := Collector{Runner: localexec.ExecRunner{}}
	got := c.version(context.Background(), "brine-fixture-nonexistent-executable-8da960f7", nil)
	if got.Status != target.Absent {
		t.Fatalf("missing tool status = %s", got.Status)
	}
}

func TestRealRunnerPermissionAndRuntimeFailuresStayUnknown(t *testing.T) {
	for _, tt := range []struct {
		name, content string
		mode          fs.FileMode
	}{
		{"permission", "#!/bin/sh\nexit 0\n", 0600},
		{"runtime", "#!/bin/sh\nexit 7\n", 0700},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tool")
			if err := os.WriteFile(path, []byte(tt.content), tt.mode); err != nil {
				t.Fatal(err)
			}
			c := Collector{Runner: localexec.ExecRunner{}}
			got := c.version(context.Background(), path, nil)
			if got.Status != target.Unknown {
				t.Fatalf("failed tool status = %s", got.Status)
			}
		})
	}
}

func TestAppUnitActiveObservation(t *testing.T) {
	for _, state := range []string{"active", "inactive", "failed", "", "unexpected"} {
		t.Run(state, func(t *testing.T) {
			f := baseFixture()
			f.files["/etc/passwd"] = "brine:x:1001:1001::/home/brine:/bin/sh\n"
			f.files["/proc/self/status"] = "Uid:\t1001\t1001\t1001\t1001\n"
			dir := "/home/brine/.config/containers/systemd"
			f.dirs[dir] = []fs.DirEntry{fixtureEntry("brine-api.container")}
			f.files[dir+"/brine-api.container"] = "[Container]\nImage=example.test/api:latest\n"
			runner := fakeRunner{"uname -m": "aarch64", "podman --remote=false secret ls --format {{.ID}} {{.Name}}": "", "systemctl --user show brine-api.service --property=ActiveState --value": state}
			s, e := (Collector{FS: f, Runner: runner, IdentityKey: []byte("fixture")}).Collect(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			active := (*s.Apps.Value)[0].UnitActive
			if active == nil {
				t.Fatal("missing measurement")
			}
			if state == "active" || state == "inactive" || state == "failed" {
				if active.Value == nil || *active.Value != (state == "active") {
					t.Fatal(active)
				}
			} else if active.Status != target.Unknown || active.Value != nil {
				t.Fatal(active)
			}
			encoded, e := target.Encode(s)
			if e != nil {
				t.Fatal(e)
			}
			decoded, e := target.Decode(encoded)
			if e != nil || (*decoded.Apps.Value)[0].UnitActive.Status != active.Status {
				t.Fatal(decoded, e)
			}
		})
	}
}
