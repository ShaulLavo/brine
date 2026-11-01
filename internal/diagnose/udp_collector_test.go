//go:build linux

package diagnose

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type udpCollectorFS struct{ fstest.MapFS }

func (f udpCollectorFS) ReadFile(_ context.Context, path string) ([]byte, error) {
	return fs.ReadFile(f.MapFS, strings.TrimPrefix(path, "/"))
}
func (f udpCollectorFS) ReadDir(_ context.Context, path string) ([]fs.DirEntry, error) {
	return fs.ReadDir(f.MapFS, strings.TrimPrefix(path, "/"))
}
func (udpCollectorFS) Readlink(_ context.Context, path string) (string, error) {
	if path == "/proc/4242/fd/6" {
		return "socket:[77777]", nil
	}
	return "", fs.ErrPermission
}

type udpCollectorRunner map[string]string

func (r udpCollectorRunner) CaptureStdout(_ context.Context, limit int, path string, args ...string) (localexec.Capture, error) {
	out, ok := r[strings.Join(append([]string{path}, args...), " ")]
	if !ok {
		return localexec.Capture{}, fs.ErrNotExist
	}
	return localexec.Capture{Stdout: out, Overflow: len(out) > limit}, nil
}

type udpCollectorStore struct{ udpStore }

func (s udpCollectorStore) ReleaseByID(context.Context, string, string) (ops.Release, error) {
	return s.release, nil
}
func (s udpCollectorStore) LastOperation(context.Context, string) (ops.Operation, error) {
	return ops.Operation{}, store.ErrNotFound
}
func (s udpCollectorStore) SavePlan(context.Context, plan.Plan, policy.Desired) (string, error) {
	return "", errors.New("unexpected write")
}

type udpHealthyProbe struct{}

func (udpHealthyProbe) Check(context.Context, target.Port, policy.Health) (bool, error) {
	return true, nil
}

func TestCollectorToStatusDiagnosisAndPlannerMissingUDPInode(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "synthetic complete proof", true: "actual Pi missing-inode shape"}[missing], func(t *testing.T) { checkUDPCollectorConsumers(t, missing) })
	}
}
func checkUDPCollectorConsumers(t *testing.T, missing bool) {
	t.Helper()
	id := "sha256:" + strings.Repeat("a", 64)
	unit := "# Brine-owned plan=" + id + "\n[Container]\nPublishPort=127.0.0.1:20080:8080\nPublishPort=192.0.2.10:4433:7777/udp\n"
	sum := sha256.Sum256([]byte(unit))
	units := []target.Unit{{Name: "demo.container", Hash: "sha256:" + hex.EncodeToString(sum[:])}}
	key := make([]byte, 51)
	key[3] = 11
	copy(key[4:15], "ssh-ed25519")
	key[18] = 32
	account := strings.Join([]string{"brine", "x", "1001", "1001", "", "/home/brine", "/bin/sh"}, ":") + "\n"
	files := map[string]string{"etc/os-release": "ID=debian\nVERSION_ID=13\n", "etc/machine-id": strings.Repeat("1", 32), "etc/ssh/ssh_host_ed25519_key.pub": "ssh-ed25519 " + base64.StdEncoding.EncodeToString(key) + " fixture", "etc/passwd": account, "proc/self/status": "Uid:\t1001\t1001\t1001\t1001\n", "proc/4242/cgroup": "0::/user.slice/user-1001.slice/user@1001.service/app.slice/demo.service/runtime\n", "proc/4242/stat": "4242 (pasta) S " + strings.Repeat("0 ", 18) + "123 0\n", "home/brine/.config/containers/systemd/demo.container": unit}
	f := udpCollectorFS{fstest.MapFS{}}
	for path, text := range files {
		f.MapFS[path] = &fstest.MapFile{Data: []byte(text)}
	}
	format := `{"name":{{json .Name}},"running":{{json .State.Running}},"unit":{{json (index .Config.Labels "PODMAN_SYSTEMD_UNIT")}},"network":{{json .HostConfig.NetworkMode}},"ports":{{json .NetworkSettings.Ports}}}`
	listener := `UNCONN 0 0 192.0.2.10:4433 0.0.0.0:* users:(("pasta",pid=4242,fd=6))`
	if !missing {
		listener += " ino:77777 sk:1"
	}
	r := udpCollectorRunner{"uname -m": "aarch64", "passt --version": "passt 1", "ss -H -ltnpe": "", "ss -H -lunpe": listener, "ip -j addr show": `[{"addr_info":[{"local":"192.0.2.10"}]}]`, "podman --remote=false secret ls --format {{.ID}} {{.Name}}": "", "systemctl --user show demo.service --property=ActiveState --value": "active", "podman --remote=false inspect --type container --format " + format + " systemd-demo": `{"name":"systemd-demo","running":true,"unit":"demo.service","network":"pasta","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"20080"}],"7777/udp":[{"HostIp":"192.0.2.10","HostPort":"4433"}]}}`}
	allocation := plan.UDPAllocation{Endpoint: plan.UDPEndpoint{Family: "ipv4", BindAddress: "192.0.2.10", HostPort: 4433}, ContainerPort: 7777, ClientAddress: "relay.example.test", Exposure: spec.UDPPublic}
	desired := policy.Desired{SchemaVersion: 1, PolicyVersion: "fixture", PolicyHash: id, Name: "demo", Image: spec.ImageReference("ghcr.io/team/demo@" + id), ContainerPort: 8080, AppPorts: policy.PortRange{Min: 20080, Max: 20090}, UDP: &policy.UDPDesired{ContainerPort: 7777, Exposure: spec.UDPPublic, Grant: policy.UDPGrant{App: "demo", Exposure: spec.UDPPublic, BindAddress: "192.0.2.10", ClientAddress: "relay.example.test", HostPorts: policy.PortRange{Min: 4433, Max: 4433}}}}
	allocation.GrantHash = desired.UDP.Grant.Hash()
	current := plan.CurrentRelease{Image: plan.Image{Digest: id, Platform: target.Platform{OS: "linux", Arch: "arm64"}, ManifestDigest: target.Known(id)}, Secrets: []plan.SecretBinding{}, CaddyFile: target.CaddyFile{Name: "demo.caddy", Hash: id}, App: "demo", ID: "release", HostPort: 20080, Units: units, Desired: desired, UDP: &allocation}
	state := plan.BrineState{Releases: []plan.CurrentRelease{current}, UDP: &plan.UDPState{Incarnations: []plan.AppIncarnation{{App: "demo", ID: id}}, Reservations: []plan.UDPReservation{{ID: "udp-op", App: "demo", Incarnation: id, State: "active", Endpoint: allocation.Endpoint}}}}
	control := target.ControlInventory{Generation: 1, Apps: []target.ControlApp{{Name: "demo", Status: target.KnownStatus, UDP: &target.ControlUDPRelease{Incarnation: id, PlanID: id, Units: units, Publication: target.UDPPublication{BindAddress: "192.0.2.10", HostPort: 4433, ContainerPort: 7777, NetworkMode: "pasta"}}}}, UDPReservations: []target.ControlUDPReservation{{App: "demo", State: "active", Family: "ipv4", Incarnation: id, BindAddress: "192.0.2.10", HostPort: 4433}}}
	collector := inventory.Collector{FS: f, Runner: r, IdentityKey: []byte("fixture"), StateInventory: func(context.Context) (target.ControlInventory, error) { return control, nil }}
	source := udpCollectorStore{udpStore{fakeStore: fakeStore{names: []string{"demo"}, release: ops.Release{ID: "release", PlanID: id, HostPort: 20080, Units: units}}, state: state, allocation: allocation}}
	report, err := (apps.Service{Inventory: collector, Store: source, Probe: udpHealthyProbe{}}).Status(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	status := report.Apps[0]
	if status.Health.Direct != "healthy" || status.Ready() == missing || status.UDP.Remote != "unverified" {
		t.Fatalf("HTTP versus UDP %+v", status)
	}
	diagnostic, err := (Reader{Inventory: collector, Store: source}).Read(t.Context(), Request{App: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if (diagnostic.Apps[0].UDP.Local == "ready") == missing {
		t.Fatal(diagnostic.Apps[0])
	}
	snapshot, err := collector.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	ready, err := target.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	ready.Apps = snapshot.Apps
	ready.UDPSockets = snapshot.UDPSockets
	ready.LocalAddresses = snapshot.LocalAddresses
	state.Target = ready.Identity
	p, err := plan.Build(plan.Input{Desired: desired, Snapshot: ready, State: state, Image: current.Image})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.Conflicts, plan.Diagnostic{Code: plan.UDPExecutionUnavailable, Field: "udp"}) {
		t.Fatal("activation guard lost", p.Conflicts)
	}
	if slices.Contains(p.Conflicts, plan.Diagnostic{Code: plan.UDPPortOwned, Field: "udp.endpoint"}) != missing {
		t.Fatal("planner did not consume UDP proof", p.Conflicts)
	}
}
