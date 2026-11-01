//go:build linux && udp_runtime

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

const udpRuntimeImage = "docker.io/nginxinc/nginx-unprivileged@sha256:7377697a821c131a924a7105fafbe7414db4e9fcc77a6f08f776f33f141ec3f8"
const udpRuntimeManifest = "sha256:e00b7e2763a0dfec9ec6d99253612510c253df47d7218cdd35c4e465b4e9ad1f"
const udpRuntimeApp = "p08-udp-runtime"

// This fixture tests transport publication, not a UDP game protocol or readiness.
func TestIsolatedUDPRuntime(t *testing.T) {
	if os.Getenv("BRINE_UDP_RUNTIME_TEST") != "1" {
		t.Skip("requires explicit disposable host authorization")
	}
	if !filepath.IsAbs(os.Getenv("BRINE_UDP_RUNTIME_WORK")) {
		t.Fatal("requires a task-owned cleanup receipt directory")
	}
	if os.Getuid() == 0 || runtime.GOARCH != "arm64" {
		t.Fatal("requires an unprivileged arm64 runner")
	}
	identity, err := user.Current()
	udpRequire(t, err)
	f := udpRuntimeFixture{t: t, identity: identity}
	f.requireAbsent()
	f.recordBaseline()
	f.installCleanup()
	f.pullImage()
	for _, address := range []string{"127.0.0.1", "::1"} {
		t.Run(address, func(t *testing.T) { child := f; child.t = t; child.runMapping(address) })
	}
}

type udpRuntimeFixture struct {
	t        *testing.T
	identity *user.User
}

func udpRequire(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func udpHash(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
func (f udpRuntimeFixture) command(path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := (localexec.ExecRunner{}).Execute(ctx, localexec.Command{Path: path, Args: args, Timeout: 90 * time.Second, OutputLimit: 256 << 10})
	if err != nil {
		f.t.Logf("%s %v failed: %s", path, args, result.Stderr)
	}
	return strings.TrimSpace(result.Stdout), err
}
func (f udpRuntimeFixture) run(path string, args ...string) string {
	f.t.Helper()
	out, err := f.command(path, args...)
	udpRequire(f.t, err)
	return out
}
func (f udpRuntimeFixture) unitPath() string {
	return filepath.Join(f.identity.HomeDir, quadlet.ActiveDirectory, udpRuntimeApp+".container")
}
func (f udpRuntimeFixture) requireAbsent() {
	_, err := os.Lstat(f.unitPath())
	if !os.IsNotExist(err) {
		f.t.Fatal("fixture unit must be absent")
	}
	names := strings.Fields(f.run("podman", "--remote=false", "ps", "-a", "--format", "{{.Names}}"))
	if slices.Contains(names, "systemd-"+udpRuntimeApp) {
		f.t.Fatal("fixture container already exists")
	}
}
func (f udpRuntimeFixture) recordBaseline() {
	for _, probe := range [][]string{
		{"podman", "--remote=false", "ps", "-a", "--format", "{{.Names}}"},
		{"podman", "--remote=false", "images", "--format", "{{.ID}}"},
		{"systemctl", "--user", "list-units", "--type=service", "--state=running", "--no-legend", "--no-pager"},
		{"ss", "-H", "-ltnpe"}, {"ss", "-H", "-lunpe"},
	} {
		raw := f.run(probe[0], probe[1:]...)
		f.t.Logf("baseline %s count=%d hash=%s", strings.Join(probe, " "), len(strings.Split(raw, "\n")), udpHash([]byte(raw)))
	}
	f.t.Logf("platform=linux/%s podman=%s pasta=%s", runtime.GOARCH, f.run("podman", "--remote=false", "--version"), f.run("dpkg-query", "--show", "--showformat=${Version}", "passt"))
}
func (f udpRuntimeFixture) installCleanup() {
	dirs := []string{".config", ".config/containers", quadlet.ActiveDirectory, ".local", ".local/share", ".local/share/brine", ".local/share/brine/quadlet", ".local/share/brine/quadlet/staging"}
	created := []string{}
	for _, dir := range dirs {
		path := filepath.Join(f.identity.HomeDir, dir)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			created = append(created, path)
		}
	}
	f.t.Cleanup(func() { f.cleanupDirectories(created) })
}
func (f udpRuntimeFixture) cleanupDirectories(created []string) {
	for _, path := range slices.Backward(created) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			f.t.Errorf("remove owned empty directory: %v", err)
		}
	}
	f.requireAbsent()
	f.t.Log("cleanup verified fixture units, containers and owned directories absent")
}

func (f udpRuntimeFixture) pullImage() {
	idsBefore := strings.Fields(f.run("podman", "--remote=false", "images", "--no-trunc", "--format", "{{.ID}}"))
	f.t.Cleanup(func() {
		raw, err := f.command("podman", "--remote=false", "image", "inspect", "--format", "{{.Id}}", udpRuntimeImage)
		if err != nil {
			return
		}
		if !slices.Contains(idsBefore, raw) {
			f.run("podman", "--remote=false", "rmi", raw)
		}
		idsFinal := strings.Fields(f.run("podman", "--remote=false", "images", "--no-trunc", "--format", "{{.ID}}"))
		slices.Sort(idsBefore)
		slices.Sort(idsFinal)
		if !slices.Equal(idsBefore, idsFinal) {
			f.t.Error("image baseline not restored")
		}
	})
	f.run("podman", "--remote=false", "pull", udpRuntimeImage)
	uid, err := strconv.ParseUint(f.identity.Uid, 10, 32)
	udpRequire(f.t, err)
	session, err := localexec.NewSession(localexec.ExecRunner{}, uint32(uid), f.identity.HomeDir, 90*time.Second)
	udpRequire(f.t, err)
	image, err := podman.ParseImage(udpRuntimeImage)
	udpRequire(f.t, err)
	info, err := podman.New(session).Inspect(f.t.Context(), image)
	udpRequire(f.t, err)
	if info.ManifestDigest != udpRuntimeManifest || info.IndexDigest != strings.Split(udpRuntimeImage, "@")[1] || info.Platform.Architecture != "arm64" {
		f.t.Fatal("unexpected image platform/pins")
	}

	f.t.Logf("index=%s manifest=%s", udpRuntimeImage, udpRuntimeManifest)
}
func (f udpRuntimeFixture) render(address string) quadlet.Unit {
	app, err := spec.Parse([]byte("schema_version=1\nname=\"" + udpRuntimeApp + "\"\nimage=\"" + udpRuntimeImage + "\"\ncontainer_port=8080\ndomains=[\"udp-runtime.example.test\"]\n[udp]\ncontainer_port=7777\nexposure=\"tailnet\"\n"))
	udpRequire(f.t, err)
	pol, err := policy.Parse([]byte(`schema_version=1
version="p08-03-runtime"
allowed_domains=["udp-runtime.example.test"]
[[allowed_registries]]
host="docker.io"
repository_prefixes=["nginxinc"]
[app_ports]
min=20431
max=20431
[resources]
memory_mb=128
pids_limit=64
[[udp_grants]]
app="` + udpRuntimeApp + `"
exposure="tailnet"
bind_address="` + address + `"
client_address="udp-runtime.example.test"
host_ports={min=24431,max=24431}
`))
	udpRequire(f.t, err)
	d, err := policy.Normalize(app, pol)
	udpRequire(f.t, err)
	raw, err := d.CanonicalBytes()
	udpRequire(f.t, err)
	a := &plan.UDPAllocation{Endpoint: plan.UDPEndpoint{Family: d.UDP.Grant.BindFamily(), BindAddress: address, HostPort: 24431}, ContainerPort: 7777, ClientAddress: d.UDP.Grant.ClientAddress, Exposure: d.UDP.Exposure, GrantHash: d.UDP.Grant.Hash()}
	p := plan.Plan{Kind: plan.Create, App: udpRuntimeApp, Hash: udpHash([]byte(address)), DesiredHash: udpHash(raw), HostPort: 20431, Image: plan.Image{Digest: strings.Split(udpRuntimeImage, "@")[1], Platform: target.Platform{OS: "linux", Arch: "arm64"}, ManifestDigest: target.Known(udpRuntimeManifest)}, UDP: &plan.UDPPlan{Next: a}, Changes: []plan.Change{{Kind: plan.RenderQuadlet, Quadlet: &plan.Quadlet{Desired: d, UDP: a}}}}
	udpRequire(f.t, plan.ValidateUDPExecution(p, d))
	unit, err := quadlet.Render(d, p, udpRuntimeManifest)
	udpRequire(f.t, err)
	f.t.Logf("isolated typed render hash=%s; no Brine lifecycle execution", unit.Hash())
	return unit
}
func (f udpRuntimeFixture) runMapping(address string) {
	f.checkFree(address)
	unit := f.render(address)
	writeUDPRuntimeReceipt(f.t, strings.TrimPrefix(unit.Hash(), "sha256:"))
	manager, err := quadlet.NewManager(f.identity.HomeDir, udpRuntimeValidator{})
	udpRequire(f.t, err)
	f.t.Cleanup(func() { udpRequire(f.t, manager.Close()) })
	f.t.Cleanup(func() { f.teardown(manager, unit) })
	udpRequire(f.t, manager.Install(f.t.Context(), unit, ""))
	udpRequire(f.t, manager.VerifyCurrent(f.t.Context(), unit.Name(), unit.Hash()))
	f.run("systemctl", "--user", "daemon-reload")
	f.run("systemctl", "--user", "start", udpRuntimeApp+".service")
	f.waitHTTP()
	f.verifyRuntime(address, unit)
}
func (f udpRuntimeFixture) checkFree(address string) {
	tcp, err := net.Listen("tcp4", "127.0.0.1:20431")
	udpRequire(f.t, err)
	udpRequire(f.t, tcp.Close())
	udp, err := net.ListenPacket("udp", net.JoinHostPort(address, "24431"))
	udpRequire(f.t, err)
	udpRequire(f.t, udp.Close())
}
func (f udpRuntimeFixture) waitHTTP() {
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get("http://127.0.0.1:20431/")
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
		closeErr := response.Body.Close()
		if readErr == nil && closeErr == nil && response.StatusCode == 200 && strings.Contains(string(body), "Welcome to nginx!") {
			return
		}
	}
	f.t.Fatal("HTTP fixture failed")
}
func (f udpRuntimeFixture) verifyRuntime(address string, unit quadlet.Unit) {
	raw := f.run("podman", "--remote=false", "inspect", "--type", "container", "--format", `{"name":{{json .Name}},"unit":{{json (index .Config.Labels "PODMAN_SYSTEMD_UNIT")}},"network":{{json .HostConfig.NetworkMode}},"ports":{{json .NetworkSettings.Ports}},"privileged":{{json .HostConfig.Privileged}},"cap_add":{{json .HostConfig.CapAdd}},"mounts":{{json .Mounts}}}`, "systemd-"+udpRuntimeApp)
	var actual struct {
		Name, Unit, Network string
		Ports               map[string][]struct{ HostIP, HostPort string }
		CapAdd              []string `json:"cap_add"`
		Privileged          bool
		Mounts              []json.RawMessage
	}
	udpRequire(f.t, json.Unmarshal([]byte(raw), &actual))
	if actual.Name != "systemd-"+udpRuntimeApp || actual.Unit != udpRuntimeApp+".service" || actual.Network != "pasta" || actual.Privileged || len(actual.CapAdd) != 0 || len(actual.Mounts) != 0 {
		f.t.Fatalf("unexpected runtime: %s", raw)
	}
	f.verifyMapping(actual.Ports, "8080/tcp", "127.0.0.1", "20431")
	f.verifyMapping(actual.Ports, "7777/udp", address, "24431")
	bindings := 0
	for _, ports := range actual.Ports {
		bindings += len(ports)
	}
	if bindings != 2 {
		f.t.Fatalf("unexpected runtime bindings: %s", raw)
	}
	recording := &udpSocketRecorder{}
	c := inventory.Collector{FS: inventory.HostFS{}, Runner: recording, RunnerUser: f.identity.Username, IdentityKey: []byte("p08-03-disposable-runtime")}
	snapshot, err := c.Collect(f.t.Context())
	udpRequire(f.t, err)
	app := findUDPRuntimeApp(f.t, snapshot)
	if app.AllocatedHostPort.Value == nil || *app.AllocatedHostPort.Value != 20431 || app.QuadletUnits.Value == nil || !slices.Contains(*app.QuadletUnits.Value, target.Unit{Name: unit.Name(), Hash: unit.Hash()}) {
		f.t.Fatal("real collector did not bind runtime and unit")
	}
	if snapshot.PortOwners.Value == nil || !hasUDPRuntimeTCPOwner(*snapshot.PortOwners.Value) {
		f.t.Fatal("exact pinned UDP unit lost TCP socket ownership")
	}
	if snapshot.UDPSockets.Value == nil || !hasUDPRuntimeSocket(*snapshot.UDPSockets.Value, address) {
		f.t.Fatal("expected UDP host socket not measured")
	}
	f.t.Logf("actual network=pasta TCP=127.0.0.1:20431:8080 UDP=%s:24431:7777; unit/container identity, no added privileges/mounts, and TCP PID/FD/cgroup ownership verified", address)
	socket := udpSocketLine(recording.raw, address)
	if !strings.Contains(socket, "ino:") || !strings.Contains(socket, `("pasta",pid=`) {
		f.t.Fatalf("missing actual pasta socket/inode evidence: %q", socket)
	}
	f.t.Log("UDP socket inode evidence verified")
	f.t.Logf("captured ss -H -lunpe task socket: %s", socket)
	f.t.Log("UDP protocol, local readiness and remote reachability remain unverified")
}
func (f udpRuntimeFixture) verifyMapping(ports map[string][]struct{ HostIP, HostPort string }, key, address, port string) {
	values := ports[key]
	if len(values) != 1 || values[0].HostIP != address || values[0].HostPort != port {
		f.t.Fatalf("mapping %s does not match render", key)
	}
}
func findUDPRuntimeApp(t *testing.T, s target.Snapshot) target.App {
	t.Helper()
	if s.Apps.Value == nil {
		t.Fatal("fixture apps unobserved")
		return target.App{}
	}
	for _, a := range *s.Apps.Value {
		if a.Name == udpRuntimeApp {
			return a
		}
	}
	t.Fatal("fixture not found by real inventory")
	return target.App{}
}
func hasUDPRuntimeTCPOwner(owners []target.PortOwner) bool {
	for _, owner := range owners {
		if owner.App == udpRuntimeApp && owner.Port == 20431 {
			return true
		}
	}
	return false
}
func hasUDPRuntimeSocket(sockets []target.UDPSocket, address string) bool {
	for _, socket := range sockets {
		if socket.Address == address && socket.Port == 24431 && socket.Process == "pasta" {
			return true
		}
	}
	return false
}
func (f udpRuntimeFixture) cleanupCommand(path string, args ...string) string {
	out, err := f.command(path, args...)
	if err != nil {
		f.t.Errorf("cleanup %s failed: %v", path, err)
	}
	return out
}
func (f udpRuntimeFixture) teardown(manager *quadlet.Manager, unit quadlet.Unit) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f.cleanupCommand("systemctl", "--user", "stop", udpRuntimeApp+".service")
	if err := manager.Rollback(ctx, unit.Name(), unit.Hash(), ""); err != nil {
		f.t.Errorf("cleanup unit: %v", err)
	}
	f.cleanupCommand("systemctl", "--user", "daemon-reload")
	names := strings.Fields(f.cleanupCommand("podman", "--remote=false", "ps", "-a", "--format", "{{.Names}}"))
	if slices.Contains(names, "systemd-"+udpRuntimeApp) {
		f.cleanupCommand("podman", "--remote=false", "rm", "systemd-"+udpRuntimeApp)
	}
	for protocol, port := range map[string]int{"-ltnpe": 20431, "-lunpe": 24431} {
		out := f.cleanupCommand("ss", "-H", protocol, "sport", "=", ":"+strconv.Itoa(port))
		if out != "" {
			f.t.Errorf("fixture socket remains: %s", protocol)
		}
	}
	f.requireAbsent()
	f.t.Log("cleanup verified unit/container and TCP/UDP sockets absent")
}

type udpRuntimeValidator struct{}

func (v udpRuntimeValidator) Validate(ctx context.Context, c quadlet.Candidate) error {
	result, err := (localexec.ExecRunner{}).Execute(ctx, localexec.Command{Path: "/usr/lib/systemd/system-generators/podman-system-generator", Args: []string{"--user", "--dryrun"}, Env: []string{"QUADLET_UNIT_DIRS=" + c.Directory}, Timeout: 10 * time.Second, OutputLimit: 256 << 10})
	if err != nil {
		return fmt.Errorf("isolated generator: %w: %s", err, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "---"+udpRuntimeApp+".service---") {
		return fmt.Errorf("generator did not emit fixture service")
	}
	return nil
}

type udpSocketRecorder struct {
	localexec.ExecRunner
	raw string
}

func (r *udpSocketRecorder) CaptureStdout(ctx context.Context, limit int, path string, args ...string) (localexec.Capture, error) {
	out, err := r.ExecRunner.CaptureStdout(ctx, limit, path, args...)
	if path == "ss" && slices.Equal(args, []string{"-H", "-lunpe"}) {
		r.raw = out.Stdout
	}
	return out, err
}
func udpSocketLine(raw, address string) string {
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		host, port, err := net.SplitHostPort(fields[3])
		if err == nil && host == address && port == "24431" {
			return line
		}
	}
	return ""
}

func writeUDPRuntimeReceipt(t *testing.T, hash string) {
	t.Helper()
	root, err := os.OpenRoot(os.Getenv("BRINE_UDP_RUNTIME_WORK"))
	udpRequire(t, err)
	defer func() { udpRequire(t, root.Close()) }()
	file, err := root.OpenFile("unit.sha256", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	udpRequire(t, err)
	_, writeErr := file.WriteString(hash)
	syncErr := file.Sync()
	closeErr := file.Close()
	udpRequire(t, writeErr)
	udpRequire(t, syncErr)
	udpRequire(t, closeErr)
}
