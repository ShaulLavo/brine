package inventory

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func productionRoutes(t *testing.T, operator bool) (fixtureFS, fakeRunner) {
	t.Helper()
	read := func(name string) string {
		b, e := os.ReadFile("testdata/" + name)
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	f := baseFixture()
	f.files["/etc/caddy/Caddyfile"] = "{\n local_certs\n}\nimport /etc/caddy/brine/current/*.caddy\n"
	f.files["/etc/caddy/brine/current/hello.caddy"] = "hello.example.com {\n\treverse_proxy 127.0.0.1:20000\n\theader X-Content-Type-Options nosniff\n\theader Referrer-Policy no-referrer\n}\n"
	f.links["/etc/caddy/brine/current"] = "gen-1"
	f.dirs["/etc/caddy/brine/current"] = []os.DirEntry{fixtureEntry("hello.caddy")}
	whole := read("import-global.json")
	if operator {
		whole = read("import-operator.json")
		f.files["/etc/caddy/Caddyfile"] = "{\n local_certs\n}\noperator.example.test {\n respond ok\n}\nimport /etc/caddy/brine/current/*.caddy\n"
	}
	return f, fakeRunner{"caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile": whole, "caddy adapt --config /etc/caddy/brine/current/hello.caddy --adapter caddyfile": read("import-site.json"), "curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/": whole}
}

func measureRoutes(t *testing.T, f fixtureFS, r fakeRunner) target.Snapshot {
	t.Helper()
	s := target.Snapshot{CaddyConfig: unknown[target.CaddyConfigSet](), LiveCaddyFiles: unknown[[]target.LiveCaddyFile]()}
	if e := (Collector{FS: f, Runner: r}).caddy(context.Background(), &s); e != nil {
		t.Fatal(e)
	}
	return s
}

func TestProductionImportedRouteProvenance(t *testing.T) {
	for _, operator := range []bool{false, true} {
		t.Run(map[bool]string{false: "global", true: "operator"}[operator], func(t *testing.T) {
			f, r := productionRoutes(t, operator)
			s := measureRoutes(t, f, r)
			if s.LiveCaddyFiles.Value == nil {
				t.Fatal("routes unknown")
			}
			own, root := false, false
			for _, file := range *s.LiveCaddyFiles.Value {
				if file.Domains.Value == nil {
					t.Fatalf("unknown source domains: %+v", file)
				}
				if file.App == "hello" {
					own = file.Name == "hello.caddy" && strings.Join(*file.Domains.Value, ",") == "hello.example.com"
				} else {
					expected := ""
					if operator {
						expected = "operator.example.test"
					}
					root = strings.Join(*file.Domains.Value, ",") == expected
				}
			}
			if !own || !root {
				t.Fatalf("missing provenance: %+v", *s.LiveCaddyFiles.Value)
			}
		})
	}
}

func routePlanInput(t *testing.T, name string) plan.Input {
	t.Helper()
	read := func(p string) []byte {
		b, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	s, e := target.Decode(read("../target/testdata/" + name + ".json"))
	if e != nil {
		t.Fatal(e)
	}
	p, e := policy.Parse(bytes.ReplaceAll(read("../policy/testdata/operator.toml"), []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
	if e != nil {
		t.Fatal(e)
	}
	a, e := spec.Parse(bytes.ReplaceAll(read("../spec/testdata/valid-minimal.toml"), []byte("example/hello"), []byte("team/hello")))
	if e != nil {
		t.Fatal(e)
	}
	d, e := policy.Normalize(a, p)
	if e != nil {
		t.Fatal(e)
	}
	image := plan.Image{Digest: strings.Split(string(d.Image), "@")[1], Platform: target.Platform{OS: "linux", Arch: s.Arch}, ManifestDigest: unknown[string]()}
	return plan.Input{Desired: d, Snapshot: s, Image: image, State: plan.BrineState{Target: s.Identity, Generation: *s.Generation.Value, Releases: []plan.CurrentRelease{}}}
}

func TestDeployThenUpdatePlanWithProductionInventory(t *testing.T) {
	in := routePlanInput(t, "ready-arm64")
	created, e := plan.Build(in)
	if e != nil || created.Kind != plan.Create {
		t.Fatalf("first deploy: %+v %v", created, e)
	}
	in = routePlanInput(t, "one-app")
	f, r := productionRoutes(t, false)
	measured := measureRoutes(t, f, r)
	in.Snapshot.CaddyConfig = measured.CaddyConfig
	in.Snapshot.LiveCaddyFiles = measured.LiveCaddyFiles
	app := (*in.Snapshot.Apps.Value)[0]
	in.State.Releases = []plan.CurrentRelease{{App: "hello", ID: "release-0001", Desired: in.Desired, Image: in.Image, HostPort: created.HostPort, Secrets: []plan.SecretBinding{}, Units: *app.QuadletUnits.Value, CaddyFile: measured.CaddyConfig.Value.Files[0]}}
	in.Desired.Environment = []policy.Environment{{Name: "RELEASE", Value: "two"}}
	updated, e := plan.Build(in)
	if e != nil || updated.Kind != plan.Update {
		t.Fatalf("later update: %+v %v", updated, e)
	}
	for _, test := range []struct {
		name string
		edit func(fixtureFS, fakeRunner)
	}{
		{"tampered", func(f fixtureFS, r fakeRunner) { f.files["/etc/caddy/brine/current/hello.caddy"] += "# tampered\n" }},
		{"adapt failure", func(f fixtureFS, r fakeRunner) {
			delete(r, "caddy adapt --config /etc/caddy/brine/current/hello.caddy --adapter caddyfile")
		}},
		{"live mismatch", func(f fixtureFS, r fakeRunner) {
			r["curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/"] = "{}"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, r := productionRoutes(t, false)
			test.edit(f, r)
			s := measureRoutes(t, f, r)
			bad := in
			bad.Snapshot.CaddyConfig = s.CaddyConfig
			bad.Snapshot.LiveCaddyFiles = s.LiveCaddyFiles
			p, e := plan.Build(bad)
			if e != nil || p.Kind != plan.Conflict {
				t.Fatalf("unsafe update: %+v %v", p, e)
			}
		})
	}
}
