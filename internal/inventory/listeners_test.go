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
			if s.PortOwners.Value == nil || len(*s.PortOwners.Value) != 1 {
				t.Fatalf("owners=%+v", s.PortOwners)
			}
			owner := (*s.PortOwners.Value)[0]
			if (owner.App == "api") != tc.owned {
				t.Fatalf("owner=%+v", owner)
			}
			if !tc.owned && owner.App != "" {
				t.Fatalf("foreign owner=%+v", owner)
			}
			if s.Apps.Status != target.KnownStatus {
				t.Fatal("apps not observed")
			}
		})
	}
}
