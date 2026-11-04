//go:build linux

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

const (
	udpRuntimeContainers = `podman --remote=false ps --all --format {{.Names}} {{.Label "PODMAN_SYSTEMD_UNIT"}}`
	udpRuntimeInspect    = `podman --remote=false inspect --type container --format {"name":{{json .Name}},"running":{{json .State.Running}},"unit":{{json (index .Config.Labels "PODMAN_SYSTEMD_UNIT")}},"network":{{json .HostConfig.NetworkMode}},"ports":{{json .NetworkSettings.Ports}}} systemd-hello`
	udpRuntimeAdapt      = "caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile"
	udpRuntimeSiteAdapt  = "caddy adapt --config /etc/caddy/brine/current/hello.caddy --adapter caddyfile"
	udpRuntimeLive       = "curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/"
	udpRuntimeUnitPath   = "/home/brine/.config/containers/systemd/hello.container"
	udpRuntimeCgroup     = "0::/user.slice/user-1001.slice/user@1001.service/app.slice/hello.service/runtime\n"
)

type udpRuntimeFS struct{ root string }

func (f udpRuntimeFS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return (inventory.HostFS{}).ReadFile(ctx, filepath.Join(f.root, path))
}
func (f udpRuntimeFS) ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error) {
	return (inventory.HostFS{}).ReadDir(ctx, filepath.Join(f.root, path))
}
func (f udpRuntimeFS) Readlink(ctx context.Context, path string) (string, error) {
	return (inventory.HostFS{}).Readlink(ctx, filepath.Join(f.root, path))
}

type udpRuntimeRunner map[string]string

func (r udpRuntimeRunner) CaptureStdout(_ context.Context, limit int, path string, args ...string) (localexec.Capture, error) {
	out, ok := r[strings.Join(append([]string{path}, args...), " ")]
	if !ok {
		return localexec.Capture{}, fs.ErrNotExist
	}
	return localexec.Capture{Stdout: out, Overflow: len(out) > limit}, nil
}
func (r udpRuntimeRunner) Execute(_ context.Context, cmd localexec.Command) (localexec.Result, error) {
	if cmd.Mutation || len(cmd.Stdin) != 0 {
		return localexec.Result{}, fmt.Errorf("inventory attempted mutation")
	}
	command := strings.Join(append([]string{cmd.Path}, cmd.Args...), " ")
	out, ok := r[command]
	if !ok && strings.HasPrefix(command, "podman container exists ") {
		return localexec.Result{}, &localexec.Error{Kind: localexec.Failed, ExitCode: 1}
	}
	if !ok {
		return localexec.Result{}, fs.ErrNotExist
	}
	return localexec.Result{Stdout: out}, nil
}

// udpRuntime measures a fixture host with the production collector and the
// real control database. Engine adapters change only fixture files, so
// planning, admission and worker freshness all read the same evidence.
type udpRuntime struct {
	*deployRig
	t         *testing.T
	root      string
	exec      udpRuntimeRunner
	collector inventory.Collector
	unit      []byte
	units     map[string][]byte
	route     []byte
	routeGen  uint64
	active    bool
	socket    string
	proofs    *udpProofSpy
}

func newUDPRuntime(t *testing.T) *udpRuntime {
	t.Helper()
	r := newDeployRig(t)
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("Registry.Example.com:5000"), []byte("ghcr.io"))
	raw = append(raw, []byte("\n[[udp_grants]]\napp = \"hello\"\nexposure = \"public\"\nbind_address = \"192.0.2.10\"\nclient_address = \"relay.example.test\"\nhost_ports = { min = 4433, max = 4435 }\n")...)
	if r.policy.p, err = policy.Parse(raw); err != nil {
		t.Fatal(err)
	}
	r.spec += "\n[udp]\ncontainer_port = 7777\nexposure = \"public\"\n"
	r.images.image.ManifestDigest = target.Known("sha256:" + strings.Repeat("b", 64))
	h := &udpRuntime{deployRig: r, t: t, root: t.TempDir(), units: map[string][]byte{}, proofs: &udpProofSpy{}}
	h.socket = `UNCONN 0 0 192.0.2.10:4433 0.0.0.0:* users:(("pasta",pid=4242,fd=6)) uid:1001 ino:77777 sk:1 cgroup:/user.slice/user-1001.slice/user@1001.service/app.slice/hello.service <->`
	for path, content := range map[string]string{
		"/etc/os-release":                   "ID=debian\nVERSION_ID=13\n",
		"/etc/machine-id":                   strings.Repeat("1", 32),
		"/proc/self/status":                 "Uid:\t1001\t1001\t1001\t1001\n",
		"/proc/4242/stat":                   "4242 (pasta) S " + strings.Repeat("0 ", 18) + "123 0\n",
		"/proc/4242/cgroup":                 udpRuntimeCgroup,
		"/var/lib/systemd/linger/brine":     "",
		"/sys/fs/cgroup/cgroup.controllers": "cpu memory",
		"/etc/caddy/Caddyfile":              "{\n local_certs\n}\nimport /etc/caddy/brine/current/*.caddy\n",
	} {
		h.write(path, []byte(content))
	}
	h.write("/etc/passwd", []byte("brine:x:1001:1001::/home/brine:/bin/sh\n"))
	hostKey := make([]byte, 51)
	hostKey[3] = 11
	copy(hostKey[4:15], "ssh-ed25519")
	hostKey[18] = 32
	h.write("/etc/ssh/ssh_host_ed25519_key.pub", []byte("ssh-ed25519 "+base64.StdEncoding.EncodeToString(hostKey)+" fixture"))
	for _, dir := range []string{"/home/brine/.config/containers/systemd", "/home/brine/.local/state/brine", "/proc/4242/fd"} {
		if err := os.MkdirAll(filepath.Join(h.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("socket:[77777]", filepath.Join(h.root, "/proc/4242/fd/6")); err != nil {
		t.Fatal(err)
	}
	h.exec = udpRuntimeRunner{
		"uname -m": "aarch64", "systemctl --version": "systemd 257", "podman version --format json": `{"Client":{"Version":"5.4.2"}}`,
		"passt --version": "passt 0.0~git20250503.587980c", "caddy version": "2.6.2", "df -B1 --output=avail /home/brine/.local/state/brine": "Avail\n10737418240\n",
		"ss -H -ltnpe": "", "ip -j addr show": `[{"addr_info":[{"local":"192.0.2.10"}]}]`, "podman --remote=false secret ls --format {{.ID}} {{.Name}}": "",
	}
	for _, file := range []struct{ command, fixture string }{
		{"image inspect ghcr.io/team/hello@sha256:" + strings.Repeat("a", 64), "image-inspect.json"},
		{"image inspect ghcr.io/team/hello@sha256:" + strings.Repeat("b", 64), "platform-image-inspect.json"},
		{"manifest inspect ghcr.io/team/hello@sha256:" + strings.Repeat("a", 64), "manifest-inspect.json"},
	} {
		h.exec["podman "+file.command] = strings.ReplaceAll(h.read("../podman/testdata/"+file.fixture), "registry.example/app", "ghcr.io/team/hello")
	}
	h.exec["podman image exists ghcr.io/team/hello@sha256:"+strings.Repeat("a", 64)] = ""
	// Release B is the same image content published under different digests.
	for command, out := range maps.Clone(h.exec) {
		if strings.HasPrefix(command, "podman ") && strings.Contains(command, "@sha256:") {
			b := strings.NewReplacer(strings.Repeat("a", 64), strings.Repeat("d", 64), strings.Repeat("b", 64), strings.Repeat("e", 64))
			h.exec[b.Replace(command)] = b.Replace(out)
		}
	}
	h.sync()
	h.collector = inventory.Collector{FS: udpRuntimeFS{h.root}, Runner: h.exec, IdentityKey: []byte("fixture-only-key"), StateInventory: r.store.InventoryState, OperationState: r.store.OperationInventoryState}
	r.service.Inventory = h.collector
	engine := r.runner.Executor.(Executor).Engine
	engine.Units, engine.Routes = udpRuntimeUnits{h}, udpRuntimeRoutes{h}
	engine.UDPReservations, engine.UDPChecks = r.store, udpProof{store: r.store, inventory: h.proofs}
	manager := engine.Systemd.(*systemd.Fake)
	manager.StartFunc = func(context.Context, systemd.Unit) error { h.active = true; h.sync(); return nil }
	manager.StopFunc = func(context.Context, systemd.Unit) error { h.active = false; h.sync(); return nil }
	manager.IsActiveFunc = func(context.Context, systemd.Unit) (bool, error) { return h.active, nil }
	manager.ShowFunc = func(_ context.Context, u systemd.Unit) (systemd.Properties, error) {
		if u.String() != "hello.service" || h.unit == nil && !h.active {
			return systemd.Properties{}, &localexec.Error{Kind: localexec.NotFound}
		}
		if h.active {
			return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
		}
		return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
	}
	engine.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
		if h.active {
			return podman.ContainerState{Running: true, Status: "running"}, nil
		}
		return podman.ContainerState{Status: "exited"}, nil
	}
	reconciler := newReconciler(r.service, engine, manager)
	r.runner.Executor = Executor{Service: r.service, Engine: engine}
	r.runner.Reconciler = runnerReconciler{reconciler}
	r.runner.Recovery = recoveryJob(reconciler)
	r.server = dispatch.NewServer("fixture", h.collector).WithJobs(jobs.Service{Store: r.store, Launcher: r.launcher, Requester: r.service.Requester}, r.service.Authorize)
	r.server.Reconciler = reconciler
	r.server.Planner = r.service
	r.server.Config = apps.Service{Store: r.store, Inventory: h.collector, LoadPolicy: r.policy.Load}
	r.server.Apps = apps.Service{Store: r.store, Inventory: h.collector, LoadPolicy: r.policy.Load}
	return h
}

func (h *udpRuntime) write(path string, raw []byte) {
	h.t.Helper()
	path = filepath.Join(h.root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		h.t.Fatal(err)
	}
}
func (h *udpRuntime) read(path string) string {
	h.t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // Path is a checked-in test fixture.
	if err != nil {
		h.t.Fatal(err)
	}
	return string(raw)
}

// sync publishes the modeled runtime as files and command output.
func (h *udpRuntime) sync() {
	h.t.Helper()
	unitPath := filepath.Join(h.root, udpRuntimeUnitPath)
	if h.unit == nil {
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			h.t.Fatal(err)
		}
	} else {
		h.write(udpRuntimeUnitPath, h.unit)
	}
	generation := fmt.Sprintf("/etc/caddy/brine/gen-%d", h.routeGen)
	if err := os.MkdirAll(filepath.Join(h.root, generation), 0700); err != nil {
		h.t.Fatal(err)
	}
	current := filepath.Join(h.root, "/etc/caddy/brine/current")
	if err := os.Remove(current); err != nil && !os.IsNotExist(err) {
		h.t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(generation), current); err != nil {
		h.t.Fatal(err)
	}
	empty := h.read("../inventory/testdata/import-empty.json")
	h.exec[udpRuntimeAdapt], h.exec[udpRuntimeLive] = empty, empty
	delete(h.exec, udpRuntimeSiteAdapt)
	if h.route != nil {
		h.write(generation+"/hello.caddy", h.route)
		h.exec[udpRuntimeAdapt] = h.read("../inventory/testdata/import-global.json")
		h.exec[udpRuntimeLive] = h.exec[udpRuntimeAdapt]
		h.exec[udpRuntimeSiteAdapt] = strings.ReplaceAll(h.read("../inventory/testdata/import-site.json"), "127.0.0.1:20000", "127.0.0.1:"+h.httpPort())
	}
	h.exec["ss -H -lunpe"], h.exec[udpRuntimeContainers] = "", ""
	h.exec["systemctl --user show hello.service --property=ActiveState --value"] = "inactive"
	delete(h.exec, udpRuntimeInspect)
	delete(h.exec, "podman container exists systemd-hello")
	delete(h.exec, "podman container inspect systemd-hello")
	if !h.active {
		return
	}
	h.exec["ss -H -lunpe"] = h.socket
	h.exec[udpRuntimeContainers] = "systemd-hello hello.service"
	h.exec["systemctl --user show hello.service --property=ActiveState --value"] = "active"
	h.exec["podman container exists systemd-hello"] = ""
	h.exec["podman container inspect systemd-hello"] = `[{"Name":"systemd-hello","Image":"748902c9f9368aa7437b05e353c23968266b0bc882ac1d74067fc1768a102ba6","Config":{"Labels":{"PODMAN_SYSTEMD_UNIT":"hello.service"}},"State":{"Status":"running","Running":true}}]`
	h.exec[udpRuntimeInspect] = `{"name":"systemd-hello","running":true,"unit":"hello.service","network":"pasta","ports":{"3000/tcp":[{"HostIp":"127.0.0.1","HostPort":"` + h.httpPort() + `"}],"7777/udp":[{"HostIp":"192.0.2.10","HostPort":"4433"}]}}`
}

// httpPort is the loopback port pinned by the installed unit, as Podman maps it.
func (h *udpRuntime) httpPort() string {
	for _, line := range strings.Split(string(h.unit), "\n") {
		if mapping, ok := strings.CutPrefix(line, "PublishPort=127.0.0.1:"); ok {
			port, _, _ := strings.Cut(mapping, ":")
			return port
		}
	}
	return "20000"
}

type udpRuntimeUnits struct{ h *udpRuntime }

func (u udpRuntimeUnits) current() string {
	if u.h.unit == nil {
		return ""
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(u.h.unit))
}
func (u udpRuntimeUnits) VerifyCurrent(_ context.Context, _ string, hashes ...string) error {
	if !slices.Contains(hashes, u.current()) {
		return fmt.Errorf("foreign live unit")
	}
	return nil
}
func (udpRuntimeUnits) Stage(context.Context, quadlet.Unit) error { return nil }
func (u udpRuntimeUnits) Install(_ context.Context, unit quadlet.Unit, _ string) error {
	u.h.unit = unit.Bytes()
	u.h.units[unit.Hash()] = unit.Bytes()
	u.h.sync()
	return nil
}
func (u udpRuntimeUnits) Rollback(_ context.Context, _, _, previous string) error {
	u.h.unit = u.h.units[previous]
	u.h.sync()
	return nil
}
func (u udpRuntimeUnits) VerifyRemove(_ context.Context, _, hash string) error {
	if u.current() != hash {
		return fmt.Errorf("foreign live unit")
	}
	return nil
}
func (u udpRuntimeUnits) Remove(_ context.Context, _, hash string) error {
	if u.current() != hash || u.h.active {
		return fmt.Errorf("unit removal refused")
	}
	u.h.unit = nil
	u.h.sync()
	return nil
}

type udpRuntimeRoutes struct{ h *udpRuntime }

func (r udpRuntimeRoutes) Publish(ctx context.Context, before caddy.State, p plan.Plan, d policy.Desired) (caddy.Result, error) {
	published, err := fakeRoutes{r.h.policy.p}.Publish(ctx, before, p, d)
	if err != nil {
		return published, err
	}
	raw, err := caddy.Render(published.Next.Sites[p.App+".caddy"])
	if err != nil {
		return caddy.Result{}, err
	}
	r.h.route, r.h.routeGen = raw, published.Next.Generation
	r.h.sync()
	return published, nil
}
func (udpRuntimeRoutes) Restore(context.Context, caddy.State, caddy.State) error { return nil }
func (r udpRuntimeRoutes) Withdraw(_ context.Context, before caddy.State, _ string) (caddy.Result, error) {
	r.h.route, r.h.routeGen = nil, before.Generation+1
	r.h.sync()
	return caddy.Result{Outcome: caddy.Applied, Next: caddy.State{Generation: r.h.routeGen, Files: map[string]string{}, Sites: map[string]caddy.Site{}}}, nil
}
func (udpRuntimeRoutes) SettleWithdrawal(context.Context, caddy.State, caddy.State, string) error {
	return nil
}

// accept admits a stored plan through dispatch and runs its worker.
func (h *udpRuntime) accept(t *testing.T, planID, key string) (jobs.Status, error) {
	t.Helper()
	accepted := h.call(t, "apply", dispatch.ApplyArgs{PlanID: planID, IdempotencyKey: key}).Data.(jobs.Accepted)
	err := h.runner.Run(t.Context(), accepted.OperationID)
	return h.call(t, "operation", dispatch.OperationArgs{OperationID: accepted.OperationID}).Data.(jobs.Status), err
}
func (h *udpRuntime) lifecycle(t *testing.T, action plan.ChangeKind) apps.ConfigPlan {
	t.Helper()
	planned := h.call(t, "lifecycle", dispatch.LifecycleArgs{App: "hello", Action: action}).Data.(apps.ConfigPlan)
	if planned.Kind == plan.Conflict || len(planned.Conflicts) != 0 {
		t.Fatalf("%s refused at planning: %+v", action, planned.Conflicts)
	}
	return planned
}

// create deploys the first UDP release through the ordinary connected path.
func (h *udpRuntime) create(t *testing.T) {
	t.Helper()
	planned := h.call(t, "plan", dispatch.PlanArgs{Spec: h.spec}).Data.(dispatch.Planned)
	if planned.Kind != plan.Create || len(planned.Conflicts) != 0 {
		t.Fatalf("create refused: %+v", planned.Conflicts)
	}
	status, err := h.accept(t, planned.PlanID, "create")
	if err != nil || status.Operation.State != ops.Succeeded {
		t.Fatal("create failed", status.Operation.State, err, udpDrift(status))
	}
	h.requireOwned(t)
}

// requireOwned proves the ordinary read attributes the live socket to hello.
func (h *udpRuntime) requireOwned(t *testing.T) {
	t.Helper()
	snapshot, err := h.collector.Collect(t.Context())
	if err != nil || snapshot.UDPSockets.Value == nil {
		t.Fatal("UDP sockets unknown", err)
	}
	for _, socket := range *snapshot.UDPSockets.Value {
		if socket.Port == 4433 && socket.App == "hello" {
			return
		}
	}
	t.Fatal("committed UDP runtime is not owned", *snapshot.UDPSockets.Value)
}

func udpDrift(status jobs.Status) []string {
	for _, event := range status.Events {
		if event.Kind != "plan_drift" {
			continue
		}
		var drift plan.DecisionDrift
		if json.Unmarshal(event.Payload, &drift) == nil {
			return drift.Paths
		}
	}
	return nil
}
func udpFailure(status jobs.Status) string {
	for _, event := range status.Events {
		var p ops.FailurePayload
		if event.Kind == "failure" && json.Unmarshal(event.Payload, &p) == nil {
			return p.Code
		}
	}
	return ""
}

func TestUDPRemovalFromOwnedRuntimePassesWorkerFreshness(t *testing.T) {
	h := newUDPRuntime(t)
	h.create(t)
	removal := h.lifecycle(t, plan.RemoveApp)
	status, err := h.accept(t, removal.PlanID, "remove")
	if err != nil || status.Operation.State != ops.Succeeded {
		t.Fatal("owned UDP removal failed", status.Operation.State, udpFailure(status), udpDrift(status), err)
	}
	if h.unit != nil || h.route != nil || h.active {
		t.Fatal("removal left runtime behind")
	}
}

func TestUDPStopFromOwnedRuntimePassesWorkerFreshness(t *testing.T) {
	h := newUDPRuntime(t)
	h.create(t)
	stop := h.lifecycle(t, plan.StopApp)
	status, err := h.accept(t, stop.PlanID, "stop")
	if err != nil || status.Operation.State != ops.Succeeded {
		t.Fatal("owned UDP stop failed", status.Operation.State, udpFailure(status), udpDrift(status), err)
	}
	if h.active || h.unit == nil {
		t.Fatal("stop did not stop exactly the unit")
	}
}

func TestUDPLifecycleTransitionsFromOwnedRuntime(t *testing.T) {
	h := newUDPRuntime(t)
	h.create(t)
	run := func(name string, planID string) {
		t.Helper()
		status, err := h.accept(t, planID, name)
		if err != nil || status.Operation.State != ops.Succeeded {
			t.Fatal(name, "failed", status.Operation.State, udpFailure(status), udpDrift(status), err)
		}
	}
	run("restart", h.lifecycle(t, plan.RestartApp).PlanID)
	h.requireOwned(t)
	run("stop", h.lifecycle(t, plan.StopApp).PlanID)
	run("start", h.lifecycle(t, plan.StartApp).PlanID)
	h.requireOwned(t)

	// Release B keeps the active endpoint while replacing the image.
	h.images.image = plan.Image{Digest: "sha256:" + strings.Repeat("d", 64), Platform: target.Platform{OS: "linux", Arch: "arm64"}, ManifestDigest: target.Known("sha256:" + strings.Repeat("e", 64))}
	h.spec = strings.Replace(h.spec, "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("d", 64), 1)
	update := h.call(t, "plan", dispatch.PlanArgs{Spec: h.spec}).Data.(dispatch.Planned)
	if update.Kind != plan.Update || len(update.Conflicts) != 0 {
		t.Fatalf("update refused: %+v", update.Conflicts)
	}
	stored, _, err := h.store.LoadPlan(t.Context(), update.PlanID)
	if err != nil || stored.UDP == nil || stored.UDP.Keep == "" {
		t.Fatal("update did not keep its active UDP reservation", err)
	}
	run("update", update.PlanID)
	h.requireOwned(t)
	// The fixture registry resolves the selected release's pinned image again.
	h.images.image = plan.Image{Digest: "sha256:" + strings.Repeat("a", 64), Platform: target.Platform{OS: "linux", Arch: "arm64"}, ManifestDigest: target.Known("sha256:" + strings.Repeat("b", 64))}

	rollback := h.call(t, "rollback", dispatch.RollbackArgs{App: "hello"}).Data.(apps.RollbackPlan)
	if rollback.Kind == plan.Conflict || len(rollback.Conflicts) != 0 {
		t.Fatalf("rollback refused: %+v", rollback.Conflicts)
	}
	run("rollback", rollback.PlanID)
	h.requireOwned(t)
	run("remove", h.lifecycle(t, plan.RemoveApp).PlanID)
}

// Only the accepted operation's own family is exempt. Every other unfinished
// mutation and every changed ownership proof still refuses before effects.
func TestUDPWorkerFreshnessStaysClosedForForeignOwnership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, *udpRuntime, string)
	}{
		{"foreign operation for the same plan", func(t *testing.T, h *udpRuntime, planID string) {
			if _, _, err := h.store.CreateOperation(t.Context(), ops.Intent{Kind: ops.Deploy, PlanID: planID}, "udp-test", "twin"); err != nil {
				t.Fatal(err)
			}
		}},
		{"global operation", func(t *testing.T, h *udpRuntime, _ string) {
			if _, _, err := h.store.CreateOperation(t.Context(), ops.Intent{Kind: ops.Reconcile}, "udp-test", "global"); err != nil {
				t.Fatal(err)
			}
		}},
		{"socket owned by another service", func(t *testing.T, h *udpRuntime, _ string) {
			h.write("/proc/4242/cgroup", []byte(strings.Replace(udpRuntimeCgroup, "hello.service", "intruder.service", 1)))
		}},
		{"socket moved to another process", func(t *testing.T, h *udpRuntime, _ string) {
			h.socket = strings.Replace(h.socket, "ino:77777", "ino:88888", 1)
			h.sync()
		}},
		{"device-scoped socket", func(t *testing.T, h *udpRuntime, _ string) {
			h.socket = strings.Replace(h.socket, "192.0.2.10:4433", "192.0.2.10%eth0:4433", 1)
			h.sync()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, action := range []plan.ChangeKind{plan.RemoveApp, plan.StopApp} {
				h := newUDPRuntime(t)
				h.create(t)
				planned := h.lifecycle(t, action)
				accepted := h.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: "lifecycle"}).Data.(jobs.Accepted)
				tc.change(t, h, planned.PlanID)
				err := h.runner.Run(t.Context(), accepted.OperationID)
				status := h.call(t, "operation", dispatch.OperationArgs{OperationID: accepted.OperationID}).Data.(jobs.Status)
				drift := udpDrift(status)
				scoped := slices.Contains(drift, "snapshot.udp_sockets.value[0].interface")
				if err == nil || status.Operation.State != ops.Failed || udpFailure(status) != "stale_plan" || !slices.Contains(drift, "snapshot.udp_sockets.value[0].app") || scoped != (tc.name == "device-scoped socket") {
					t.Fatal(action, "accepted unproved ownership", status.Operation.State, udpFailure(status), drift, err)
				}
				if !h.active || h.unit == nil || h.route == nil {
					t.Fatal(action, "refusal had runtime effects")
				}
			}
		})
	}
}
