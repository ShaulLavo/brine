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
	r["uname -m"] = "aarch64"
	s, e := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture-only-key")}).Collect(context.Background())
	if e != nil {
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
	freshFS, freshRunner := productionRoutes(t, false)
	freshFS.links["/etc/caddy/brine/current"] = "gen-0"
	freshFS.dirs["/etc/caddy/brine/current"] = []os.DirEntry{}
	empty, e := os.ReadFile("testdata/import-empty.json")
	if e != nil {
		t.Fatal(e)
	}
	freshRunner["caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile"] = string(empty)
	freshRunner["curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/"] = string(empty)
	fresh := measureRoutes(t, freshFS, freshRunner)
	in.Snapshot.CaddyConfig = fresh.CaddyConfig
	in.Snapshot.LiveCaddyFiles = fresh.LiveCaddyFiles
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
		{"changed valid renderer output", func(f fixtureFS, r fakeRunner) {
			f.files["/etc/caddy/brine/current/hello.caddy"] = strings.ReplaceAll(f.files["/etc/caddy/brine/current/hello.caddy"], "20000", "20001")
			for key, value := range r {
				r[key] = strings.ReplaceAll(value, "20000", "20001")
			}
		}},
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

func TestOperatorRootDomainsStillRefusePlans(t *testing.T) {
	for _, test := range []struct{ name, fixture, site, domain string }{
		{"separate site", "import-operator.json", "operator.example.test", "operator.example.test"},
		{"same domain on HTTP", "import-overlap.json", "http://hello.example.com", "hello.example.com"},
		{"catchall", "import-catchall.json", ":8443", "hello.example.com"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, r := productionRoutes(t, false)
			whole, e := os.ReadFile("testdata/" + test.fixture)
			if e != nil {
				t.Fatal(e)
			}
			response := "protected"
			if test.fixture == "import-operator.json" {
				response = "ok"
			}
			f.files["/etc/caddy/Caddyfile"] = "{\n local_certs\n}\n" + test.site + " {\n respond " + response + "\n}\nimport /etc/caddy/brine/current/*.caddy\n"
			r["caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile"] = string(whole)
			r["curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/"] = string(whole)
			s := measureRoutes(t, f, r)
			in := routePlanInput(t, "ready-arm64")
			in.Desired.Name = "newapp"
			in.Desired.Domains = []spec.Domain{spec.Domain(test.domain)}
			in.Snapshot.CaddyConfig = s.CaddyConfig
			in.Snapshot.LiveCaddyFiles = s.LiveCaddyFiles
			p, e := plan.Build(in)
			if e != nil {
				t.Fatal(e)
			}
			found := false
			for _, conflict := range p.Conflicts {
				if conflict.Code == plan.DomainOwned {
					found = true
				}
			}
			if p.Kind != plan.Conflict || !found {
				t.Fatalf("foreign domain accepted: %+v", p)
			}
		})
	}
}

func TestCaddyProvenanceFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(fixtureFS, fakeRunner)
	}{
		{"unmatched imported route", func(f fixtureFS, r fakeRunner) {
			r["caddy adapt --config /etc/caddy/brine/current/hello.caddy --adapter caddyfile"] = strings.ReplaceAll(r["caddy adapt --config /etc/caddy/brine/current/hello.caddy --adapter caddyfile"], "127.0.0.1:20000", "127.0.0.1:20001")
		}},
		{"null imported server", func(f fixtureFS, r fakeRunner) {
			r["caddy adapt --config /etc/caddy/brine/current/hello.caddy --adapter caddyfile"] = `{"apps":{"http":{"servers":{"srv0":null}}}}`
		}},
		{"null live server", func(f fixtureFS, r fakeRunner) {
			cfg := `{"apps":{"http":{"servers":{"srv0":null}}}}`
			r["caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile"] = cfg
			r["curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/"] = cfg
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, r := productionRoutes(t, false)
			test.edit(f, r)
			s := measureRoutes(t, f, r)
			if s.LiveCaddyFiles.Status != target.Unknown {
				t.Fatalf("ambiguous routes trusted: %+v", s.LiveCaddyFiles)
			}
		})
	}
}

func TestGenerationFileMustMatchRendererAndMeasuredHash(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(fixtureFS)
	}{
		{"operator file using app name", func(f fixtureFS) {
			f.files["/etc/caddy/brine/current/hello.caddy"] = "hello.example.com {\n respond protected\n}\n"
		}},
		{"comment appended", func(f fixtureFS) { f.files["/etc/caddy/brine/current/hello.caddy"] += "# operator\n" }},
		{"unknown generation", func(f fixtureFS) { f.links["/etc/caddy/brine/current"] = "not-a-generation" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, r := productionRoutes(t, false)
			test.edit(f)
			s := measureRoutes(t, f, r)
			if s.LiveCaddyFiles.Value == nil {
				return
			}
			for _, file := range *s.LiveCaddyFiles.Value {
				if file.App != "" {
					t.Fatalf("unattributable file trusted: %+v", file)
				}
			}
		})
	}
}

func TestImportedOperatorFileCannotBorrowAppProvenance(t *testing.T) {
	f, r := productionRoutes(t, false)
	f.files["/etc/caddy/Caddyfile"] = "{\n local_certs\n}\nimport /etc/caddy/operator.caddy\n"
	f.files["/etc/caddy/operator.caddy"] = f.files["/etc/caddy/brine/current/hello.caddy"]
	f.dirs["/etc/caddy"] = []os.DirEntry{fixtureEntry("operator.caddy")}
	r["caddy adapt --config /etc/caddy/operator.caddy --adapter caddyfile"] = r["caddy adapt --config /etc/caddy/brine/current/hello.caddy --adapter caddyfile"]
	s := measureRoutes(t, f, r)
	if s.LiveCaddyFiles.Value == nil {
		t.Fatal("foreign file not observed")
	}
	for _, file := range *s.LiveCaddyFiles.Value {
		if file.App != "" {
			t.Fatalf("foreign file attributed: %+v", file)
		}
	}
	in := routePlanInput(t, "ready-arm64")
	in.Desired.Name = "newapp"
	in.Snapshot.CaddyConfig = s.CaddyConfig
	in.Snapshot.LiveCaddyFiles = s.LiveCaddyFiles
	p, e := plan.Build(in)
	if e != nil {
		t.Fatal(e)
	}
	for _, conflict := range p.Conflicts {
		if conflict.Code == plan.DomainOwned {
			return
		}
	}
	t.Fatalf("foreign file ownership lost: %+v", p)
}
