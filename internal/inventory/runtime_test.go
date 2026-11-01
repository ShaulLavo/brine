//go:build linux

package inventory

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

const testRuntimePortFormat = `{"name":{{json .Name}},"running":{{json .State.Running}},"unit":{{json (index .Config.Labels "PODMAN_SYSTEMD_UNIT")}},"network":{{json .HostConfig.NetworkMode}},"ports":{{json .NetworkSettings.Ports}}}`

func TestRenderedUnitDiscoveryAndLivePort(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		known        bool
	}{
		{"running", `{"name":"systemd-api","running":true,"unit":"api.service","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"20080"}]}}`, true},
		// Podman 5.4 inspect includes image-exposed ports even without publication:
		// null/empty arrays are not HostIp/HostPort bindings.
		{"published with null exposed port", `{"name":"systemd-api","running":true,"unit":"api.service","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"20080"}],"9090/tcp":null}}`, true},
		{"published with empty exposed port", `{"name":"systemd-api","running":true,"unit":"api.service","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"20080"}],"9090/tcp":[]}}`, true},
		{"only unbound exposed ports", `{"name":"systemd-api","running":true,"unit":"api.service","ports":{"8080/tcp":null,"9090/tcp":[]}}`, false},
		{"multiple bindings on one port", `{"name":"systemd-api","running":true,"unit":"api.service","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"20080"},{"HostIp":"127.0.0.1","HostPort":"20081"}]}}`, false},
		{"published UDP", `{"name":"systemd-api","running":true,"unit":"api.service","ports":{"8080/udp":[{"HostIp":"127.0.0.1","HostPort":"20080"}]}}`, false},
		{"invalid published protocol", `{"name":"systemd-api","running":true,"unit":"api.service","ports":{"8080/invalid":[{"HostIp":"127.0.0.1","HostPort":"20080"}]}}`, false},
		{"invalid published container port", `{"name":"systemd-api","running":true,"unit":"api.service","ports":{"invalid/tcp":[{"HostIp":"127.0.0.1","HostPort":"20080"}]}}`, false},
		{"invalid published host port", `{"name":"systemd-api","running":true,"unit":"api.service","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"invalid"}]}}`, false},
		{"stopped", `{"name":"systemd-api","running":false,"unit":"api.service","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"20080"}]}}`, false},
		{"wrong unit", `{"name":"systemd-api","running":true,"unit":"other.service","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"20080"}]}}`, false},
		{"off loopback", `{"name":"systemd-api","running":true,"unit":"api.service","ports":{"8080/tcp":[{"HostIp":"0.0.0.0","HostPort":"20080"}]}}`, false},
		{"multiple", `{"name":"systemd-api","running":true,"unit":"api.service","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"20080"}],"8081/tcp":[{"HostIp":"127.0.0.1","HostPort":"20081"}]}}`, false},
		{"malformed", `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := secretOnlyFixture()
			dir := "/home/brine/.config/containers/systemd"
			f.dirs[dir] = []fs.DirEntry{fixtureEntry("api.container"), fixtureEntry("foreign.container")}
			f.files[dir+"/api.container"] = "# Brine-owned plan=sha256:" + strings.Repeat("a", 64) + "\n[Container]\nPublishPort=127.0.0.1:20080:8080\n"
			f.files[dir+"/foreign.container"] = "[Container]\nImage=foreign\n"
			r := fakeRunner{"uname -m": "aarch64", "podman --remote=false secret ls --format {{.ID}} {{.Name}}": "", "podman --remote=false inspect --type container --format " + testRuntimePortFormat + " systemd-api": tc.output}
			snap, e := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}).Collect(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			if snap.Apps.Value == nil || len(*snap.Apps.Value) != 1 || (*snap.Apps.Value)[0].Name != "api" {
				t.Fatal("renderer-owned unit not discovered")
			}
			app := (*snap.Apps.Value)[0]
			if tc.known {
				if app.AllocatedHostPort.Value == nil || *app.AllocatedHostPort.Value != target.Port(20080) {
					t.Fatal("live port not measured")
				}
			} else if app.AllocatedHostPort.Status != target.Unknown {
				t.Fatal("unverified port guessed")
			}
		})
	}
}

const udpRuntimeRecord = `{"name":"systemd-api","running":true,"unit":"api.service","network":"pasta","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"20080"}],"7777/udp":[{"HostIp":"192.0.2.10","HostPort":"4433"}]}}`

func TestUDPRuntimePublication(t *testing.T) {
	for _, tc := range []udpRuntimeCase{
		{"ipv4", "", "", true},
		{"ipv6", "192.0.2.10", "2001:db8::10", true},
		{"missing network", `"pasta"`, `""`, false},
		{"host network", "pasta", "host", false},
		{"slirp network", "pasta", "slirp4netns", false},
		{"wildcard", "192.0.2.10", "0.0.0.0", false},
		{"mapped address", "192.0.2.10", "::ffff:192.0.2.10", false},
		{"zone", "192.0.2.10", "fe80::1%eth0", false},
		{"noncanonical host port", "4433", "04433", false},
		{"noncanonical container port", "7777/udp", "07777/udp", false},
		{"extra udp", `"ports":{`, `"ports":{"8888/udp":[{"HostIp":"192.0.2.11","HostPort":"4434"}],`, false},
		{"extra tcp", `"ports":{`, `"ports":{"8888/tcp":[{"HostIp":"127.0.0.1","HostPort":"20081"}],`, false},
		{"extra binding", `"HostPort":"4433"}`, `"HostPort":"4433"},{"HostIp":"192.0.2.11","HostPort":"4434"}`, false},
		{"wrong container", "systemd-api", "systemd-other", false},
	} {
		t.Run(tc.name, tc.check)
	}
}

type udpRuntimeCase struct {
	name, old, new string
	known          bool
}

func (tc udpRuntimeCase) check(t *testing.T) {
	out := udpRuntimeRecord
	if tc.old != "" {
		out = strings.ReplaceAll(out, tc.old, tc.new)
	}
	r := fakeRunner{"podman --remote=false inspect --type container --format " + testRuntimePortFormat + " systemd-api": out}
	got, _ := (Collector{Runner: r}).livePublication(t.Context(), []target.Unit{{Name: "api.container"}})
	if (got.Status == target.KnownStatus) != tc.known {
		t.Fatalf("publication=%+v", got)
	}
	if !tc.known {
		return
	}
	address := "192.0.2.10"
	if tc.name == "ipv6" {
		address = "2001:db8::10"
	}
	if *got.Value != (publication{Host: 20080, Container: 8080, UDPAddress: address, UDPHost: 4433, UDPContainer: 7777, NetworkMode: "pasta"}) {
		t.Fatalf("mapping=%+v", got.Value)
	}
}

func TestPinnedUDPMapping(t *testing.T) {
	const tcp = "PublishPort=127.0.0.1:20080:8080\n"
	const udp = "PublishPort=192.0.2.10:4433:7777/udp\n"
	for _, tc := range []udpPinnedCase{
		{"tcp", tcp, true}, {"ipv4", tcp + udp, true}, {"ipv6", tcp + "PublishPort=[2001:db8::10]:4433:7777/udp\n", true},
		{"no tcp", udp, false}, {"duplicate tcp", tcp + tcp, false}, {"duplicate udp", tcp + udp + udp, false},
		{"unknown protocol", tcp + strings.ReplaceAll(udp, "/udp", "/sctp"), false},
		{"wildcard", tcp + strings.ReplaceAll(udp, "192.0.2.10", "0.0.0.0"), false},
		{"unbracketed ipv6", tcp + strings.ReplaceAll(udp, "192.0.2.10", "2001:db8::10"), false},
		{"noncanonical port", strings.ReplaceAll(tcp, "20080", "020080"), false},
		{"explicit tcp", strings.TrimSpace(tcp) + "/tcp\n", false},
		{"range", tcp + strings.ReplaceAll(udp, "4433", "4433-4434"), false},
		{"udp after service", tcp + "[Service]\n" + udp, true},
	} {
		t.Run(tc.name, tc.check)
	}
}

type udpPinnedCase struct {
	name, lines string
	valid       bool
}

func (tc udpPinnedCase) check(t *testing.T) {
	got, ok := pinnedListenerPort("[Container]\n" + tc.lines)
	if ok != tc.valid {
		t.Fatalf("mapping=%+v valid=%v", got, ok)
	}
	if ok && (got.Host != 20080 || got.Container != 8080) {
		t.Fatal(got)
	}
}

func TestPinnedUDPPublicationBindsExactRuntime(t *testing.T) {
	for _, field := range []string{"matching", "address", "host port", "container port", "missing UDP"} {
		t.Run(field, func(t *testing.T) { checkPinnedUDPBinding(t, field) })
	}
}
func checkPinnedUDPBinding(t *testing.T, field string) {
	f := secretOnlyFixture()
	path := "/home/brine/.config/containers/systemd/api.container"
	data := "# Brine-owned plan=sha256:" + strings.Repeat("a", 64) + "\n[Container]\nPublishPort=127.0.0.1:20080:8080\nPublishPort=192.0.2.10:4433:7777/udp\n"
	f.files[path] = data
	actual := publication{Host: 20080, Container: 8080, UDPAddress: "192.0.2.10", UDPHost: 4433, UDPContainer: 7777, NetworkMode: "pasta"}
	switch field {
	case "address":
		actual.UDPAddress = "192.0.2.11"
	case "host port":
		actual.UDPHost++
	case "container port":
		actual.UDPContainer++
	case "missing UDP":
		actual.UDPAddress = ""
		actual.UDPHost = 0
		actual.UDPContainer = 0
	}
	snapshot := target.Snapshot{Apps: target.Known([]target.App{{Name: "api", AllocatedHostPort: target.Known(target.Port(20080)), QuadletUnits: target.Known([]target.Unit{{Name: "api.container", Hash: digest([]byte(data))}})}})}
	got := (Collector{FS: f, RunnerUser: "brine"}).listenerBindings(t.Context(), &snapshot, "/home/brine", map[string]publication{"api": actual})
	if (len(got) == 1) != (field == "matching") {
		t.Fatalf("unbound publication=%+v", got)
	}
}

func TestStoppedPublicationRequiresExplicitCompleteRuntime(t *testing.T) {
	for _, tc := range []struct {
		record string
		want   target.Status
	}{
		{`{"name":"systemd-api","running":false,"unit":"api.service","ports":{}}`, target.Absent},
		{`{"name":"systemd-api","unit":"api.service","ports":{}}`, target.Unknown},
		{`{"name":"systemd-api","running":false,"unit":"api.service"}`, target.Unknown},
		{`{"name":"systemd-api","running":false,"unit":"api.service","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"20080"}]}}`, target.Unknown},
	} {
		r := fakeRunner{"podman --remote=false inspect --type container --format " + testRuntimePortFormat + " systemd-api": tc.record}
		got, _ := (Collector{Runner: r}).livePublication(t.Context(), []target.Unit{{Name: "api.container"}})
		if got.Status != tc.want {
			t.Fatalf("runtime %s: got %s, want %s", tc.record, got.Status, tc.want)
		}
	}
}
