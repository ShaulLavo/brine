//go:build linux

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
	const listener = `LISTEN 0 128 127.0.0.1:20080 0.0.0.0:* users:(("pasta",pid=4242,fd=6)) ino:77777 sk:1`
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
		{"container-side drift", cgroup, listener, "127.0.0.1:20080:8080", false},
		{"unrestricted published address", cgroup, listener, "0.0.0.0:20080:8080", false},
		{"duplicate publication", cgroup, listener, "127.0.0.1:20080:8080\nPublishPort=127.0.0.1:20081:8080", false},
		{"unknown listening process", cgroup, "LISTEN 0 128 127.0.0.1:20080 0.0.0.0:*", "127.0.0.1:20080:8080", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := secretOnlyFixture()
			dir := "/home/brine/.config/containers/systemd"
			f.dirs[dir] = []fs.DirEntry{fixtureEntry("api.container")}
			f.files[dir+"/api.container"] = "# Brine-owned plan=sha256:" + strings.Repeat("a", 64) + "\n[Container]\nPublishPort=" + tc.publish + "\n"
			f.files["/proc/4242/stat"] = testProcessStat("123")
			f.links["/proc/4242/fd/6"] = "socket:[77777]"
			if tc.cgroup != "" {
				f.files["/proc/4242/cgroup"] = tc.cgroup
			}
			r := fakeRunner{
				"uname -m": "aarch64", "ss -H -ltnpe": tc.listener, "ss -H -lunp": "",
				"podman --remote=false secret ls --format {{.ID}} {{.Name}}":                                    "",
				"podman --remote=false inspect --type container --format " + runtimePortFormat + " systemd-api": `{"name":"systemd-api","running":true,"unit":"api.service","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"20080"}]}}`,
			}
			if tc.name == "container-side drift" {
				key := "podman --remote=false inspect --type container --format " + runtimePortFormat + " systemd-api"
				r[key] = strings.ReplaceAll(r[key], "8080/tcp", "9090/tcp")
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
			if got := (Collector{FS: f, RunnerUser: "brine"}).listenerBindings(context.Background(), &s, "/home/brine", map[string]publication{"api": {Host: 20080, Container: 8080}}); len(got) != 0 {
				t.Fatalf("unverified binding=%+v", got)
			}
		})
	}
}

type changingProcessFS struct {
	fixtureFS
	stats   []string
	sockets []string
	groups  []string
}

func (f *changingProcessFS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	var rows *[]string
	switch path {
	case "/proc/4242/stat":
		rows = &f.stats
	case "/proc/4242/cgroup":
		rows = &f.groups
	}
	if rows != nil && len(*rows) > 0 {
		value := (*rows)[0]
		if len(*rows) > 1 {
			*rows = (*rows)[1:]
		}
		return []byte(value), nil
	}
	return f.fixtureFS.ReadFile(ctx, path)
}
func (f *changingProcessFS) Readlink(ctx context.Context, path string) (string, error) {
	if path == "/proc/4242/fd/6" && len(f.sockets) > 0 {
		value := f.sockets[0]
		if len(f.sockets) > 1 {
			f.sockets = f.sockets[1:]
		}
		return value, nil
	}
	return f.fixtureFS.Readlink(ctx, path)
}
func testProcessStat(start string) string {
	return "4242 (pasta (fixture)) S " + strings.Repeat("0 ", 18) + start + " 0\n"
}

func TestSocketIdentityBeforeAppAttribution(t *testing.T) {
	const group = "0::/user.slice/user-1001.slice/user@1001.service/app.slice/api.service/runtime\n"
	const ss = `LISTEN 0 128 127.0.0.1:20080 0.0.0.0:* users:(("foreign",pid=4242,fd=6)) ino:77777 sk:1`
	for _, tc := range []struct {
		name, output           string
		stats, sockets, groups []string
		owned                  bool
	}{
		{"stable socket", ss, []string{testProcessStat("123")}, []string{"socket:[77777]"}, nil, true},
		{"PID reused after ss", ss, []string{testProcessStat("124")}, []string{"socket:[88888]"}, nil, false},
		{"PID reused during proof", ss, []string{testProcessStat("123"), testProcessStat("124")}, []string{"socket:[77777]"}, nil, false},
		{"socket transferred during proof", ss, []string{testProcessStat("123")}, []string{"socket:[77777]", "socket:[88888]"}, nil, false},
		{"cgroup changed", ss, []string{testProcessStat("123")}, []string{"socket:[77777]"}, []string{group, "0::/system.slice/foreign.service\n"}, false},
		{"inode unavailable", strings.ReplaceAll(ss, "ino:77777 ", ""), []string{testProcessStat("123")}, []string{"socket:[77777]"}, nil, false},
		{"stat unavailable", ss, nil, []string{"socket:[77777]"}, nil, false},
		{"malformed stat", ss, []string{"4242 (fixture) S 0"}, []string{"socket:[77777]"}, nil, false},
		{"descriptor unreadable", ss, []string{testProcessStat("123")}, nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &changingProcessFS{fixtureFS: secretOnlyFixture(), stats: tc.stats, sockets: tc.sockets, groups: tc.groups}
			dir := "/home/brine/.config/containers/systemd"
			f.dirs[dir] = []fs.DirEntry{fixtureEntry("api.container")}
			f.files[dir+"/api.container"] = "# Brine-owned plan=sha256:" + strings.Repeat("a", 64) + "\n[Container]\nPublishPort=127.0.0.1:20080:8080\n"
			f.files["/proc/4242/cgroup"] = group
			r := fakeRunner{"uname -m": "aarch64", "ss -H -ltnpe": tc.output, "ss -H -lunp": "", "podman --remote=false secret ls --format {{.ID}} {{.Name}}": "", "podman --remote=false inspect --type container --format " + runtimePortFormat + " systemd-api": `{"name":"systemd-api","running":true,"unit":"api.service","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"20080"}]}}`}
			snapshot, err := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}).Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.PortOwners.Value == nil || len(*snapshot.PortOwners.Value) != 1 {
				t.Fatal("missing observed socket")
			}
			owner := (*snapshot.PortOwners.Value)[0]
			if (owner.App == "api") != tc.owned {
				t.Fatalf("unproven socket ownership=%+v", owner)
			}
		})
	}
}
