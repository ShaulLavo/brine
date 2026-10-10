package inventory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestVersionCaptureBoundary(t *testing.T) {
	for _, size := range []int{4095, 4096, 4097} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			out := "2.6.2" + strings.Repeat(" ", size-len("2.6.2"))
			c := Collector{Runner: fakeRunner{"caddy version": out}}
			got := c.version(context.Background(), "caddy", []string{"version"})
			want := target.KnownStatus
			if size > 4096 {
				want = target.Unknown
			}
			if got.Status != want {
				t.Fatalf("%d bytes: status = %s, want %s", size, got.Status, want)
			}
		})
	}
}

func TestLargeSecretCollection(t *testing.T) {
	f := baseFixture()
	f.files["/etc/passwd"] = "brine:x:1001:1001::/home/brine:/bin/sh\n"
	f.files["/proc/self/status"] = "Uid:\t1001\t1001\t1001\t1001\n"
	var out strings.Builder
	for i := range 160 {
		fmt.Fprintf(&out, "immutable-secret-%03d brine.app-%03d.token.v1\n", i, i)
	}
	if out.Len() <= 4096 {
		t.Fatal("fixture must exceed the former capture limit")
	}
	r := fakeRunner{"podman --remote=false secret ls --format {{.ID}} {{.Name}}": out.String()}
	s := target.Snapshot{Runner: target.Runner{User: target.Known("brine")}, Apps: unknown[[]target.App]()}
	(Collector{FS: f, Runner: r, RunnerUser: "brine"}).apps(context.Background(), &s, "/home/brine", true, nil)
	if s.Apps.Value == nil || len(*s.Apps.Value) != 160 {
		t.Fatalf("apps status = %s, want 160 complete apps", s.Apps.Status)
	}
	for i, app := range *s.Apps.Value {
		if app.Name != fmt.Sprintf("app-%03d", i) || app.Secrets.Value == nil || len(*app.Secrets.Value) != 1 || (*app.Secrets.Value)[0].ID != fmt.Sprintf("immutable-secret-%03d", i) {
			t.Fatalf("app %d lost secret evidence", i)
		}
	}
}

func TestLargeListenerCollection(t *testing.T) {
	var out strings.Builder
	for i := range 160 {
		fmt.Fprintf(&out, "LISTEN 0 128 *:%d *:* users:((\"app-%03d\",pid=%d,fd=3))\n", 20000+i, i, 1000+i)
	}
	if out.Len() <= 4096 {
		t.Fatal("fixture must exceed the former capture limit")
	}
	for _, protocol := range []string{"-ltnpe", "-lunp"} {
		t.Run(protocol, func(t *testing.T) {
			r := fakeRunner{"ss -H -ltnpe": "", "ss -H -lunp": ""}
			r["ss -H "+protocol] = out.String()
			if protocol == "-lunp" {
				r["ss -H "+protocol] = strings.ReplaceAll(out.String(), "LISTEN 0 128", "UNCONN 0 0")
			}
			s := target.Snapshot{UsedPorts: unknown[[]target.Port](), PortOwners: unknown[[]target.PortOwner]()}
			udp := (Collector{FS: baseFixture(), Runner: r}).listeners(context.Background(), &s, "/home/brine", nil)
			if s.UsedPorts.Value == nil || len(*s.UsedPorts.Value) != 160 || s.PortOwners.Value == nil || udp.Value == nil {
				t.Fatal("listener collection is incomplete")
			}
			if (*s.UsedPorts.Value)[159] != 20159 || (protocol == "-ltnpe" && len(*s.PortOwners.Value) != 160) || (protocol == "-lunp" && len(*udp.Value) != 160) {
				t.Fatal("listener tail was lost")
			}
		})
	}
}

func TestLargeCaddyCollection(t *testing.T) {
	var out strings.Builder
	out.WriteString(`{"apps":{"http":{"servers":{"fixture":{"routes":[`)
	for i := range 160 {
		if i > 0 {
			out.WriteByte(',')
		}
		fmt.Fprintf(&out, `{"match":[{"host":["app-%03d.example.test"]}],"handle":[{"handler":"static_response","body":"fixture"}]}`, i)
	}
	out.WriteString(`]}}}}}`)
	if out.Len() <= 4096 {
		t.Fatal("fixture must exceed the former capture limit")
	}
	f := baseFixture()
	f.dirs["/etc/caddy"] = []fs.DirEntry{fixtureEntry("sites.caddy")}
	f.files["/etc/caddy/Caddyfile"] = "import /etc/caddy/sites.caddy\n"
	f.files["/etc/caddy/sites.caddy"] = "# synthetic multi-app fixture\n"
	r := fakeRunner{
		"caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile":                         out.String(),
		"caddy adapt --config /etc/caddy/sites.caddy --adapter caddyfile":                       out.String(),
		"curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/": out.String(),
	}
	s := target.Snapshot{LiveCaddyFiles: unknown[[]target.LiveCaddyFile]()}
	if err := (Collector{FS: f, Runner: r}).caddy(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if s.LiveCaddyFiles.Value == nil || len(*s.LiveCaddyFiles.Value) != 2 {
		t.Fatalf("live Caddy status = %s, want complete routes", s.LiveCaddyFiles.Status)
	}
	var domains target.Observation[[]string]
	for _, file := range *s.LiveCaddyFiles.Value {
		if file.Domains.Value != nil && len(*file.Domains.Value) == 160 {
			domains = file.Domains
		}
	}
	if domains.Value == nil || len(*domains.Value) != 160 || (*domains.Value)[159] != "app-159.example.test" {
		t.Fatal("Caddy route tail was lost")
	}
}

func TestCollectionCaptureBoundaries(t *testing.T) {
	for _, probe := range []struct {
		name  string
		limit int
	}{
		{"secret", secretOutputLimit},
		{"listener", listenerOutputLimit},
		{"container", containerOutputLimit},
		{"caddy", caddyOutputLimit},
		{"runtime", runtimeOutputLimit},
	} {
		limit := probe.limit
		for _, size := range []int{limit - 1, limit, limit + 1} {
			t.Run(fmt.Sprintf("%s/%d", probe.name, size), func(t *testing.T) {
				c := Collector{Runner: fakeRunner{"fixture": strings.Repeat("x", size)}}
				out, err := c.probe(context.Background(), limit, "fixture")
				if size > limit {
					if !errors.Is(err, localexec.ErrOutputLimit) || out != "" {
						t.Fatalf("overflow returned authority: bytes=%d error=%v", len(out), err)
					}
				} else if err != nil || len(out) != size {
					t.Fatalf("complete output rejected: bytes=%d error=%v", len(out), err)
				}
			})
		}
	}
}

func TestSecretOverflowKeepsAppsUnknown(t *testing.T) {
	f := baseFixture()
	f.files["/etc/passwd"] = "brine:x:1001:1001::/home/brine:/bin/sh\n"
	f.files["/proc/self/status"] = "Uid:\t1001\t1001\t1001\t1001\n"
	const record = "fixture-id brine.api.token.v1\n"
	for _, size := range []int{secretOutputLimit, secretOutputLimit + 1} {
		r := fakeRunner{"podman --remote=false secret ls --format {{.ID}} {{.Name}}": record + strings.Repeat(" ", size-len(record))}
		s := target.Snapshot{Runner: target.Runner{User: target.Known("brine")}, Apps: unknown[[]target.App]()}
		(Collector{FS: f, Runner: r, RunnerUser: "brine"}).apps(context.Background(), &s, "/home/brine", true, nil)
		if size == secretOutputLimit {
			if s.Apps.Value == nil || len(*s.Apps.Value) != 1 || (*s.Apps.Value)[0].Secrets.Value == nil {
				t.Fatal("exact-limit secret collection rejected")
			}
		} else if s.Apps.Status != target.Unknown || s.Apps.Value != nil {
			t.Fatal("overflow granted partial secret authority")
		}
	}
}

func TestListenerOverflowKeepsPortsUnknown(t *testing.T) {
	const tcpRecord = "LISTEN 0 128 *:20000 *:*\n"
	for _, protocol := range []string{"-ltnpe", "-lunp"} {
		record := tcpRecord
		if protocol == "-lunp" {
			record = "UNCONN 0 0 *:20000 *:*\n"
		}
		for _, size := range []int{listenerOutputLimit, listenerOutputLimit + 1} {
			r := fakeRunner{"ss -H -ltnpe": tcpRecord, "ss -H -lunp": ""}
			r["ss -H "+protocol] = record + strings.Repeat(" ", size-len(record))
			s := target.Snapshot{UsedPorts: unknown[[]target.Port](), PortOwners: unknown[[]target.PortOwner]()}
			udp := (Collector{FS: baseFixture(), Runner: r}).listeners(context.Background(), &s, "/home/brine", nil)
			if size == listenerOutputLimit {
				if s.UsedPorts.Value == nil || len(*s.UsedPorts.Value) != 1 || s.PortOwners.Value == nil || udp.Value == nil {
					t.Fatal("exact-limit listeners rejected")
				}
			} else if s.UsedPorts.Status != target.Unknown || s.PortOwners.Status != target.Unknown || udp.Status != target.Unknown {
				t.Fatal("overflow granted partial listener authority")
			}
		}
	}
}

func TestCaddyOverflowKeepsRoutesUnknown(t *testing.T) {
	const config = `{"apps":{"http":{"servers":{"fixture":{"routes":[{"match":[{"host":["app.example.test"]}]}]}}}}}`
	const live = "curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/"
	const disk = "caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile"
	const imported = "caddy adapt --config /etc/caddy/sites.caddy --adapter caddyfile"
	for _, probe := range []string{live, disk, imported} {
		for _, size := range []int{caddyOutputLimit, caddyOutputLimit + 1} {
			r := fakeRunner{live: config, disk: config, imported: config}
			r[probe] = config + strings.Repeat(" ", size-len(config))
			f := baseFixture()
			f.dirs["/etc/caddy"] = []fs.DirEntry{fixtureEntry("sites.caddy")}
			f.files["/etc/caddy/Caddyfile"] = "import /etc/caddy/sites.caddy\n"
			f.files["/etc/caddy/sites.caddy"] = "# synthetic fixture\n"
			s := target.Snapshot{LiveCaddyFiles: unknown[[]target.LiveCaddyFile]()}
			if err := (Collector{FS: f, Runner: r}).caddy(context.Background(), &s); err != nil {
				t.Fatal(err)
			}
			if size == caddyOutputLimit {
				if s.LiveCaddyFiles.Value == nil || len(*s.LiveCaddyFiles.Value) != 2 {
					t.Fatal("exact-limit Caddy routes rejected")
				}
			} else if s.LiveCaddyFiles.Status != target.Unknown || s.LiveCaddyFiles.Value != nil {
				t.Fatal("overflow granted partial Caddy authority")
			}
		}
	}
}
