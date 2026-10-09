package inventory

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

// These shapes were captured from Podman 5.4.2's pasta listener on Debian 13.
// The PID, service name and port are replaced with synthetic fixture values.
func TestRootlessListenerOwnership(t *testing.T) {
	const cgroup = "0::/user.slice/user-1001.slice/user@1001.service/app.slice/api.service/runtime\n"
	const listener = `LISTEN 0 128 127.0.0.1:20080 0.0.0.0:* users:(("pasta",pid=4242,fd=6))`
	for _, tc := range []struct {
		name, cgroup, listener, publish string
		owned                           bool
	}{
		{"pasta service descendant", cgroup, listener, "127.0.0.1:20080:8080", true},
		{"rootlessport service descendant", cgroup, strings.ReplaceAll(listener, "pasta", "rootlessport"), "127.0.0.1:20080:8080", true},
		{"foreign service", strings.ReplaceAll(cgroup, "api.service", "foreign.service"), listener, "127.0.0.1:20080:8080", false},
		{"foreign listener sharing port", cgroup, listener + "\n" + strings.ReplaceAll(listener, "4242", "4243"), "127.0.0.1:20080:8080", true},
		{"non-loopback socket", cgroup, strings.ReplaceAll(listener, "127.0.0.1:20080", "0.0.0.0:20080"), "127.0.0.1:20080:8080", false},
		{"ambiguous cgroups", cgroup + "0::/system.slice/foreign.service\n", listener, "127.0.0.1:20080:8080", false},
		{"system unit with same name", "0::/system.slice/api.service\n", listener, "127.0.0.1:20080:8080", false},
		{"other user", strings.ReplaceAll(cgroup, "1001", "1002"), listener, "127.0.0.1:20080:8080", false},
		{"service prefix collision", strings.ReplaceAll(cgroup, "api.service/", "api.service-other/"), listener, "127.0.0.1:20080:8080", false},
		{"missing cgroup", "", listener, "127.0.0.1:20080:8080", false},
		{"different pinned port", cgroup, listener, "127.0.0.1:20081:8080", false},
		{"unrestricted published address", cgroup, listener, "0.0.0.0:20080:8080", false},
		{"duplicate publication", cgroup, listener, "127.0.0.1:20080:8080\nPublishPort=127.0.0.1:20081:8080", false},
		{"unknown listening process", cgroup, "LISTEN 0 128 127.0.0.1:20080 0.0.0.0:*", "127.0.0.1:20080:8080", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := secretOnlyFixture()
			dir := "/home/brine/.config/containers/systemd"
			f.dirs[dir] = []fs.DirEntry{fixtureEntry("api.container")}
			f.files[dir+"/api.container"] = "# Brine-owned plan=sha256:" + strings.Repeat("a", 64) + "\n[Container]\nPublishPort=" + tc.publish + "\n"
			if tc.cgroup != "" {
				f.files["/proc/4242/cgroup"] = tc.cgroup
			}
			r := fakeRunner{
				"uname -m": "aarch64", "ss -H -ltnp": tc.listener, "ss -H -lunp": "",
				"podman --remote=false secret ls --format {{.ID}} {{.Name}}":                                    "",
				"podman --remote=false inspect --type container --format " + runtimePortFormat + " systemd-api": `{"name":"systemd-api","running":true,"unit":"api.service","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"20080"}]}}`,
			}
			s, e := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}).Collect(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			expected := 1
			if strings.Contains(tc.listener, "4243") {
				expected = 2
			}
			if s.PortOwners.Value == nil || len(*s.PortOwners.Value) != expected {
				t.Fatalf("owners=%+v", s.PortOwners)
			}
			owned := 0
			for _, owner := range *s.PortOwners.Value {
				if owner.App == "api" {
					owned++
				} else if owner.App != "" {
					t.Fatalf("foreign owner=%+v", owner)
				}
			}
			if (owned == 1) != tc.owned || owned > 1 {
				t.Fatalf("owners=%+v", *s.PortOwners.Value)
			}
			if s.Apps.Status != target.KnownStatus {
				t.Fatal("apps not observed")
			}
		})
	}
}

func TestListenerBindingRequiresUnchangedOwnedUnit(t *testing.T) {
	const path = "/home/brine/.config/containers/systemd/api.container"
	original := "# Brine-owned plan=sha256:" + strings.Repeat("a", 64) + "\n[Container]\nPublishPort=127.0.0.1:20080:8080\n"
	for _, tc := range []struct{ name, data string }{
		{"changed bytes", original + "Environment=CHANGED=yes\n"},
		{"unreadable", ""},
		{"unowned", "[Container]\nPublishPort=127.0.0.1:20080:8080\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := secretOnlyFixture()
			hash := digest([]byte(original))
			if tc.data != "" {
				f.files[path] = tc.data
			}
			if tc.name == "unowned" {
				hash = digest([]byte(tc.data))
			}
			s := target.Snapshot{Apps: target.Known([]target.App{{Name: "api", AllocatedHostPort: target.Known(target.Port(20080)), QuadletUnits: target.Known([]target.Unit{{Name: "api.container", Hash: hash}})}})}
			if got := (Collector{FS: f, RunnerUser: "brine"}).listenerBindings(context.Background(), &s, "/home/brine"); len(got) != 0 {
				t.Fatalf("unverified binding=%+v", got)
			}
		})
	}
}
