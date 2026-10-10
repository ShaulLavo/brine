//go:build linux

package store

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/target"
)

type inventoryFixtureFS struct{ root string }

func (f inventoryFixtureFS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return (inventory.HostFS{}).ReadFile(ctx, filepath.Join(f.root, path))
}
func (f inventoryFixtureFS) ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error) {
	return (inventory.HostFS{}).ReadDir(ctx, filepath.Join(f.root, path))
}
func (f inventoryFixtureFS) Readlink(ctx context.Context, path string) (string, error) {
	return (inventory.HostFS{}).Readlink(ctx, filepath.Join(f.root, path))
}

type inventoryFixtureRunner map[string]string

func (r inventoryFixtureRunner) RunStdout(_ context.Context, path string, args ...string) (string, error) {
	out, ok := r[strings.Join(append([]string{path}, args...), " ")]
	if !ok {
		return "", fs.ErrNotExist
	}
	return out, nil
}
func (r inventoryFixtureRunner) Execute(ctx context.Context, cmd localexec.Command) (localexec.Result, error) {
	if cmd.Mutation || len(cmd.Stdin) != 0 {
		return localexec.Result{}, fmt.Errorf("inventory attempted mutation")
	}
	out, err := r.RunStdout(ctx, cmd.Path, cmd.Args...)
	return localexec.Result{Stdout: out}, err
}

func TestCreateRemoveRecreateThroughProductionCollector(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	for path, content := range map[string]string{
		"/etc/os-release":                   "ID=debian\nVERSION_ID=13\n",
		"/etc/machine-id":                   strings.Repeat("1", 32),
		"/etc/ssh/ssh_host_ed25519_key.pub": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA fixture",
		"/etc/passwd":                       "brine:x:1001:1001::/home/brine:/bin/sh\n",
		"/proc/self/status":                 "Uid:\t1001\t1001\t1001\t1001\n",
		"/var/lib/systemd/linger/brine":     "",
		"/sys/fs/cgroup/cgroup.controllers": "cpu memory",
		"/etc/caddy/Caddyfile":              "{\n local_certs\n}\nimport /etc/caddy/brine/current/*.caddy\n",
	} {
		write(path, content)
	}
	for _, dir := range []string{"/home/brine/.config/containers/systemd", "/home/brine/.local/state/brine", "/etc/caddy/brine/current"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// The collector only needs a measured generation link; fixtures never use
	// real host paths, sockets, credentials or subprocesses.
	if err := os.Rename(filepath.Join(root, "/etc/caddy/brine/current"), filepath.Join(root, "/etc/caddy/brine/gen-0")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("gen-0", filepath.Join(root, "/etc/caddy/brine/current")); err != nil {
		t.Fatal(err)
	}
	const secretsCommand = "podman --remote=false secret ls --format {{.ID}} {{.Name}}"
	const containersCommand = `podman --remote=false ps --all --format {{.Names}} {{.Label "PODMAN_SYSTEMD_UNIT"}}`
	const liveConfigCommand = "curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/"
	const adaptCommand = "caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile"
	empty := read("../inventory/testdata/import-empty.json")
	runner := inventoryFixtureRunner{
		"uname -m": "aarch64", "systemctl --version": "systemd 257", "podman version --format json": `{"Client":{"Version":"5.4.2"}}`,
		"passt --version": "passt 0.0~git20250503.587980c", "caddy version": "2.6.2", "df -B1 --output=avail /home/brine/.local/state/brine": "Avail\n10737418240\n",
		"ss -H -ltnpe": "", "ss -H -lunp": "", secretsCommand: "fixture-id brine.hello.db.v1", containersCommand: "", adaptCommand: empty, liveConfigCommand: empty,
	}
	collector := inventory.Collector{FS: inventoryFixtureFS{root}, Runner: runner, IdentityKey: []byte("fixture-only-key"), StateInventory: s.InventoryState}
	in := fixture(t)
	in.Desired.Secrets = []policy.Secret{{Name: "DB", Reference: "db"}}
	facts := func() plan.Input {
		t.Helper()
		var err error
		in.Snapshot, err = collector.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		in.State, err = s.LoadBrineState(ctx, in.Snapshot.Identity, *in.Snapshot.Generation.Value)
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	install := func(p plan.Plan, id string) Release {
		t.Helper()
		if p.Kind != plan.Create {
			t.Fatalf("create refused: %+v", p.Conflicts)
		}
		if _, err := s.SavePlan(ctx, p, in.Desired); err != nil {
			t.Fatal(err)
		}
		unit, err := quadlet.Render(in.Desired, p, *p.Image.ManifestDigest.Value)
		if err != nil {
			t.Fatal(err)
		}
		write("/home/brine/.config/containers/systemd/hello.container", string(unit.Bytes()))
		site, err := caddy.CommittedSite(in.Desired, 20000)
		if err != nil {
			t.Fatal(err)
		}
		route, err := caddy.Render(site)
		if err != nil {
			t.Fatal(err)
		}
		write("/etc/caddy/brine/current/hello.caddy", string(route))
		runner[adaptCommand] = read("../inventory/testdata/import-global.json")
		runner[liveConfigCommand] = runner[adaptCommand]
		runner["caddy adapt --config /etc/caddy/brine/current/hello.caddy --adapter caddyfile"] = read("../inventory/testdata/import-site.json")
		runner[containersCommand] = "systemd-hello hello.service"
		runner["systemctl --user show hello.service --property=ActiveState --value"] = "active"
		for _, file := range []struct{ command, fixture string }{
			{"image inspect ghcr.io/team/hello@" + p.Image.Digest, "image-inspect.json"},
			{"image inspect ghcr.io/team/hello@" + *p.Image.ManifestDigest.Value, "platform-image-inspect.json"},
			{"manifest inspect ghcr.io/team/hello@" + p.Image.Digest, "manifest-inspect.json"},
		} {
			runner["podman "+file.command] = strings.ReplaceAll(read("../podman/testdata/"+file.fixture), "registry.example/app", "ghcr.io/team/hello")
		}
		runner["podman container exists systemd-hello"] = ""
		runner["podman image exists ghcr.io/team/hello@"+p.Image.Digest] = ""
		runner["podman container inspect systemd-hello"] = `[{"Name":"systemd-hello","Image":"748902c9f9368aa7437b05e353c23968266b0bc882ac1d74067fc1768a102ba6","Config":{"Labels":{"PODMAN_SYSTEMD_UNIT":"hello.service"}},"State":{"Status":"running","Running":true}}]`
		runner[`podman --remote=false inspect --type container --format {"name":{{json .Name}},"running":{{json .State.Running}},"unit":{{json (index .Config.Labels "PODMAN_SYSTEMD_UNIT")}},"ports":{{json .NetworkSettings.Ports}}} systemd-hello`] = `{"name":"systemd-hello","running":true,"unit":"hello.service","ports":{"3000/tcp":[{"HostIp":"127.0.0.1","HostPort":"20000"}]}}`
		r := Release{ID: id, PlanID: p.Hash, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: []target.Unit{{Name: unit.Name(), Hash: unit.Hash()}}, CaddyFile: target.CaddyFile{Name: "hello.caddy", Hash: digest(route)}, CaddyGeneration: 0}
		if err = s.CommitRelease(ctx, "hello", r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	created, err := plan.Build(facts())
	if err != nil {
		t.Fatal(err)
	}
	first := install(created, "first")
	removal, err := plan.BuildRemove(facts())
	if err != nil || removal.Kind != plan.Update {
		t.Fatal(removal.Conflicts, err)
	}
	if _, err = s.SavePlan(ctx, removal, in.Desired); err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: removal.Hash}, "fixture", "remove")
	if err != nil {
		t.Fatal(err)
	}
	if fresh, err := plan.BuildRemove(facts()); err != nil || fresh.Hash != removal.Hash {
		t.Fatal("queued removal changed its preflight facts", fresh.Conflicts, err)
	}
	advanceRemoval(t, s, op.ID)
	for _, path := range []string{"/home/brine/.config/containers/systemd/hello.container", "/etc/caddy/brine/current/hello.caddy"} {
		if err = os.Remove(filepath.Join(root, path)); err != nil {
			t.Fatal(err)
		}
	}
	runner[containersCommand], runner[adaptCommand], runner[liveConfigCommand] = "", empty, empty
	if err = s.RetireApp(ctx, op.ID, "hello", first.ID); err != nil {
		t.Fatal(err)
	}
	if p, err := plan.Build(facts()); err != nil || p.Kind != plan.Conflict {
		t.Fatal("unfinished retirement accepted", p, err)
	}
	if err = s.SetOperationState(ctx, op.ID, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	noop, err := plan.BuildRemove(facts())
	if err != nil || noop.Kind != plan.NoOp {
		t.Fatal("retained resources prevent no-op removal", noop.Conflicts, err)
	}
	if _, err = s.SavePlan(ctx, noop, in.Desired); err != nil {
		t.Fatal(err)
	}
	noopOp, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: noop.Hash}, "fixture", "remove-noop")
	if err != nil {
		t.Fatal(err)
	}
	if fresh, err := plan.BuildRemove(facts()); err != nil || fresh.Hash != noop.Hash {
		t.Fatal("queued no-op changed absence evidence", fresh.Conflicts, err)
	}
	for _, state := range []ops.State{ops.Preflight, ops.Succeeded} {
		if err = s.SetOperationState(ctx, noopOp.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	recreated, err := plan.Build(facts())
	if err != nil || recreated.Kind != plan.Create || recreated.HostPort != first.HostPort || recreated.Secrets[0].ID != "fixture-id" || *in.Snapshot.Generation.Value != 2 {
		t.Fatal(recreated, err)
	}
	install(recreated, "recreated")
	observed := facts()
	if p, err := plan.Build(observed); err != nil || p.Kind != plan.NoOp || *observed.Snapshot.Generation.Value != 3 {
		t.Fatal("recreated app not observed as committed", p.Conflicts, err)
	}
	if _, err = s.ReleaseByID(ctx, "hello", first.ID); err != nil || runner[secretsCommand] != "fixture-id brine.hello.db.v1" {
		t.Fatal("retained history or secret changed", err)
	}
}
