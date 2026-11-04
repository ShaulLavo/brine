//go:build linux

package inventory

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/udpready"
)

const udpOwnedUnit = "# Brine-owned plan=sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + "\n[Container]\nPublishPort=127.0.0.1:20080:8080\nPublishPort=192.0.2.10:4433:7777/udp\n"
const udpGroup = "0::/user.slice/user-1001.slice/user@1001.service/app.slice/api.service/runtime\n"
const udpOwnedLine = `UNCONN 0 0 192.0.2.10:4433 0.0.0.0:* users:(("pasta",pid=4242,fd=6)) ino:77777 sk:1`

func udpCollectorFixture() (*changingProcessFS, fakeRunner, target.ControlInventory, plan.CurrentRelease, *plan.UDPState) {
	f := &changingProcessFS{fixtureFS: secretOnlyFixture()}
	dir := "/home/brine/.config/containers/systemd"
	f.dirs[dir] = []fs.DirEntry{fixtureEntry("api.container")}
	f.files[dir+"/api.container"] = udpOwnedUnit
	f.files["/proc/4242/cgroup"] = udpGroup
	f.files["/proc/4242/stat"] = testProcessStat("123")
	f.links["/proc/4242/fd/6"] = "socket:[77777]"
	f.files["/proc/4243/cgroup"] = udpGroup
	f.files["/proc/4243/stat"] = strings.ReplaceAll(testProcessStat("123"), "4242", "4243")
	f.links["/proc/4243/fd/7"] = "socket:[77778]"
	r := fakeRunner{"uname -m": "aarch64", "ss -H -ltnpe": `LISTEN 0 128 127.0.0.1:20080 *:* users:(("pasta",pid=4243,fd=7)) ino:77778`, "ss -H -lunpe": udpOwnedLine, "ip -j addr show": `[{"addr_info":[{"local":"192.0.2.10"}]}]`, "passt --version": "passt 1", "podman --remote=false secret ls --format {{.ID}} {{.Name}}": "", "systemctl --user show api.service --property=ActiveState --value": "active", "podman --remote=false inspect --type container --format " + testRuntimePortFormat + " systemd-api": udpRuntimeRecord}
	id := "sha256:" + strings.Repeat("a", 64)
	units := []target.Unit{{Name: "api.container", Hash: digest([]byte(udpOwnedUnit))}}
	allocation := plan.UDPAllocation{Endpoint: plan.UDPEndpoint{Family: "ipv4", BindAddress: "192.0.2.10", HostPort: 4433}, ContainerPort: 7777}
	current := plan.CurrentRelease{App: "api", ID: "release", Units: units, UDP: &allocation}
	state := &plan.UDPState{Incarnations: []plan.AppIncarnation{{App: "api", ID: id}}, Reservations: []plan.UDPReservation{{ID: "udp-op", App: "api", Incarnation: id, State: "active", Endpoint: allocation.Endpoint}}}
	control := target.ControlInventory{Generation: 1, Apps: []target.ControlApp{{Name: "api", Status: target.KnownStatus}}}
	return f, r, control, current, state
}

func TestUDPCollectorReadinessRequiresKernelAndDurableIdentity(t *testing.T) {
	for _, name := range []string{"owned", "missing inode on Pi", "foreign FD", "foreign cgroup", "namesake system unit", "process reuse", "FD changes", "group changes", "missing process", "missing bind address", "extra wildcard", "duplicate socket", "identical socket", "device-scoped socket", "extra binding", "unsupported mode", "wrong container", "unknown sockets", "truncated sockets", "unknown address", "stopped", "stopped unknown container", "missing head", "old incarnation", "staged", "releasing", "changed marker", "changed unit"} {
		t.Run(name, func(t *testing.T) { checkUDPCollectorCase(t, name) })
	}
}

func checkUDPCollectorCase(t *testing.T, name string) {
	t.Helper()
	f, r, control, current, state := udpCollectorFixture()
	control.Apps[0].UDP = &target.ControlUDPRelease{Incarnation: state.Incarnations[0].ID, PlanID: state.Incarnations[0].ID, Units: current.Units, Publication: target.UDPPublication{BindAddress: "192.0.2.10", HostPort: 4433, ContainerPort: 7777, NetworkMode: "pasta"}}
	control.UDPReservations = []target.ControlUDPReservation{{ID: "udp-op", App: "api", Incarnation: state.Incarnations[0].ID, State: "active", Family: "ipv4", BindAddress: "192.0.2.10", HostPort: 4433}}
	inspect := "podman --remote=false inspect --type container --format " + testRuntimePortFormat + " systemd-api"
	switch name {
	case "missing inode on Pi":
		r["ss -H -lunpe"] = strings.ReplaceAll(udpOwnedLine, " ino:77777", "")
	case "foreign FD":
		f.links["/proc/4242/fd/6"] = "socket:[88888]"
	case "foreign cgroup":
		f.files["/proc/4242/cgroup"] = strings.ReplaceAll(udpGroup, "api.service", "foreign.service")
	case "namesake system unit":
		f.files["/proc/4242/cgroup"] = "0::/system.slice/api.service\n"
	case "process reuse":
		f.stats = []string{testProcessStat("123"), testProcessStat("124")}
	case "FD changes":
		f.sockets = []string{"socket:[77777]", "socket:[88888]"}
	case "group changes":
		f.groups = []string{udpGroup, "0::/system.slice/api.service\n"}
	case "missing process":
		r["ss -H -lunpe"] = "UNCONN 0 0 192.0.2.10:4433 *:* ino:77777"
	case "missing bind address":
		r["ip -j addr show"] = "[]"
	case "extra wildcard":
		r["ss -H -lunpe"] += "\nUNCONN 0 0 [::]:4433 *:*"
	case "duplicate socket":
		r["ss -H -lunpe"] += "\n" + strings.ReplaceAll(udpOwnedLine, "77777", "88888")
	case "identical socket":
		r["ss -H -lunpe"] += "\n" + udpOwnedLine
	case "device-scoped socket":
		r["ss -H -lunpe"] = strings.Replace(udpOwnedLine, "192.0.2.10:4433", "192.0.2.10%eth0:4433", 1)
	case "extra binding":
		r[inspect] = strings.ReplaceAll(udpRuntimeRecord, `"ports":{`, `"ports":{"8888/udp":[{"HostIp":"192.0.2.10","HostPort":"4434"}],`)
	case "unsupported mode":
		r[inspect] = strings.ReplaceAll(udpRuntimeRecord, "pasta", "slirp4netns")
	case "wrong container":
		r[inspect] = strings.ReplaceAll(udpRuntimeRecord, "systemd-api", "systemd-foreign")
	case "unknown sockets":
		delete(r, "ss -H -lunpe")
	case "truncated sockets":
		r["ss -H -lunpe"] = strings.Repeat("x", listenerOutputLimit+1)
	case "unknown address":
		r["ip -j addr show"] = "["
	case "stopped", "stopped unknown container":
		r["systemctl --user show api.service --property=ActiveState --value"] = "inactive"
		delete(r, inspect)
		r["ss -H -lunpe"] = ""
		if name == "stopped" {
			r["podman --remote=false ps --all --format "+containerInventoryFormat] = ""
		}
	case "missing head":
		control.Apps[0].UDP = nil
	case "old incarnation":
		control.UDPReservations[0].Incarnation = "sha256:" + strings.Repeat("b", 64)
	case "staged", "releasing":
		control.UDPReservations[0].State = name
		control.UDPReservations[0].Owner = "operation"
	case "changed marker":
		f.files["/home/brine/.config/containers/systemd/api.container"] = strings.ReplaceAll(udpOwnedUnit, strings.Repeat("a", 64), strings.Repeat("b", 64))
	case "changed unit":
		f.files["/home/brine/.config/containers/systemd/api.container"] += "Environment=DRIFT=yes\n"
	}
	c := Collector{FS: f, Runner: r, IdentityKey: []byte("fixture"), StateInventory: func(context.Context) (target.ControlInventory, error) { return control, nil }}
	s, err := c.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result := udpready.Check(s, "api", *current.UDP, current.Units)
	status := apps.UDPStatusFor(s, state, current)
	want := udpready.NotReady
	if name == "owned" {
		want = udpready.Ready
	}
	if name == "stopped" {
		want = udpready.Stopped
	}
	if name == "unknown sockets" || name == "truncated sockets" || name == "unknown address" || name == "identical socket" {
		want = udpready.Unknown
	}
	if result.Local != want || status.Local != want || status.Remote != "unverified" {
		t.Fatalf("local=%+v status=%+v sockets=%+v app=%+v", result, status, s.UDPSockets, s.Apps)
	}
	if name == "owned" && ((*s.UDPSockets.Value)[0].App != "api" || !slices.Contains(*s.UsedPorts.Value, target.Port(4433))) {
		t.Fatal("collector did not prove UDP ownership", s)
	}
	if name == "device-scoped socket" && (s.UDPSockets.Status != target.KnownStatus || (*s.UDPSockets.Value)[0] != target.UDPSocket{Address: "192.0.2.10", Interface: "eth0", Port: 4433, Process: "pasta", Unit: "api.service"}) {
		t.Fatal("device-scoped socket was attributed or lost", s.UDPSockets)
	}
	if name == "missing inode on Pi" && ((*s.UDPSockets.Value)[0].App != "" || (*s.PortOwners.Value)[0].App != "api") {
		t.Fatal("PID/FD without UDP inode became ownership")
	}
}

func TestRetiredUDPAbsenceRequiresCompleteHistoryAndExplainsCurrentReuse(t *testing.T) {
	endpoint := target.ControlUDPEndpoint{Family: "ipv4", BindAddress: "192.0.2.10", HostPort: 4433}
	history := target.Known([]target.ControlUDPEndpoint{endpoint})
	app := target.ControlApp{Name: "old", RetiredUDP: &history}
	for _, name := range []string{"absent", "unknown sockets", "foreign remnant", "legitimate reuse", "extra wildcard", "history incomplete"} {
		t.Run(name, func(t *testing.T) { checkRetiredUDPAbsence(t, name, app) })
	}
}

func checkRetiredUDPAbsence(t *testing.T, name string, app target.ControlApp) {
	t.Helper()
	s := target.Snapshot{UDPSockets: target.Known([]target.UDPSocket{})}
	a := app
	switch name {
	case "unknown sockets":
		s.UDPSockets = unknown[[]target.UDPSocket]()
	case "foreign remnant":
		s.UDPSockets = target.Known([]target.UDPSocket{{Address: "192.0.2.10", Port: 4433}})
	case "legitimate reuse":
		s.UDPSockets = target.Known([]target.UDPSocket{{Address: "192.0.2.10", Port: 4433, App: "new"}})
	case "extra wildcard":
		s.UDPSockets = target.Known([]target.UDPSocket{{Address: "192.0.2.10", Port: 4433, App: "new"}, {Address: "::", Port: 4433}})
	case "history incomplete":
		o := unknown[[]target.ControlUDPEndpoint]()
		a.RetiredUDP = &o
	}
	if got := retiredUDPAbsent(s, a); got != (name == "absent" || name == "legitimate reuse") {
		t.Fatal(name, got)
	}
}

func TestUDPCollectorCanonicalIPv6PreservesTCPProof(t *testing.T) {
	f, r, control, current, state := udpCollectorFixture()
	unit := strings.ReplaceAll(udpOwnedUnit, "192.0.2.10", "[2001:db8::10]")
	f.files["/home/brine/.config/containers/systemd/api.container"] = unit
	current.Units = []target.Unit{{Name: "api.container", Hash: digest([]byte(unit))}}
	current.UDP.Endpoint = plan.UDPEndpoint{Family: "ipv6", BindAddress: "2001:db8::10", HostPort: 4433}
	publication := target.UDPPublication{BindAddress: "2001:db8::10", HostPort: 4433, ContainerPort: 7777, NetworkMode: "pasta"}
	control.Apps[0].UDP = &target.ControlUDPRelease{Incarnation: state.Incarnations[0].ID, PlanID: state.Incarnations[0].ID, Units: current.Units, Publication: publication}
	control.UDPReservations = []target.ControlUDPReservation{{App: "api", Incarnation: state.Incarnations[0].ID, State: "active", Family: "ipv6", BindAddress: "2001:db8::10", HostPort: 4433}}
	key := "podman --remote=false inspect --type container --format " + testRuntimePortFormat + " systemd-api"
	r[key] = strings.ReplaceAll(udpRuntimeRecord, "192.0.2.10", "2001:db8::10")
	r["ip -j addr show"] = `[{"addr_info":[{"local":"2001:db8::10"}]}]`
	r["ss -H -lunpe"] = strings.ReplaceAll(udpOwnedLine, "192.0.2.10", "[2001:0DB8::10]")
	s, err := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture"), StateInventory: func(context.Context) (target.ControlInventory, error) { return control, nil }}).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result := udpready.Check(s, "api", *current.UDP, current.Units)
	if result.Local != udpready.Ready || (*s.UDPSockets.Value)[0].Address != "2001:db8::10" || (*s.PortOwners.Value)[0].App != "api" {
		t.Fatal(result, s)
	}
}
