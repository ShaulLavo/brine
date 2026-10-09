package inventory

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

const testRuntimePortFormat = `{"name":{{json .Name}},"running":{{json .State.Running}},"unit":{{json (index .Config.Labels "PODMAN_SYSTEMD_UNIT")}},"ports":{{json .NetworkSettings.Ports}}}`

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
