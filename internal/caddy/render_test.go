package caddy

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
)

func fixturePolicy(t testing.TB) policy.Policy {
	t.Helper()
	b, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func fixtureApp(t testing.TB) spec.App {
	t.Helper()
	a, err := spec.Parse([]byte(`schema_version=1
name="hello"
image="registry.example.com:5000/team/web@sha256:` + strings.Repeat("a", 64) + `"
container_port=3000
domains=["web.example.com", "other.example.net"]
`))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func fixtureSite(t testing.TB) Site {
	t.Helper()
	s, err := NewSite(fixtureApp(t), fixturePolicy(t), 20001)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRenderGolden(t *testing.T) {
	s := fixtureSite(t)
	got, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/site.caddy")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if _, err := Render(Site{}); err == nil {
		t.Fatal("zero site accepted")
	}
}

func TestSiteRefusesForgedFields(t *testing.T) {
	p := fixturePolicy(t)
	for _, attack := range []string{"a.example.com\nimport /tmp/evil", "a.example.com {", "a.example.com}", `a.example.com"`, "{env.DOMAIN}", "a.example.com#comment", "import", "a.example.com, evil.example.com", "a.example.com\radmin off", "*.example.com"} {
		t.Run(attack, func(t *testing.T) {
			a := fixtureApp(t)
			a.Domains = []spec.Domain{spec.Domain(attack)}
			if _, err := NewSite(a, p, 20001); err == nil {
				t.Fatal("injection accepted")
			}
		})
	}
	for _, port := range []spec.Port{0, 80, 65535} {
		if _, err := NewSite(fixtureApp(t), p, port); err == nil {
			t.Fatal("out-of-policy port accepted")
		}
	}
	a := fixtureApp(t)
	a.Name = "../escape"
	if _, err := NewSite(a, p, 20001); err == nil {
		t.Fatal("unsafe name accepted")
	}
	if _, err := NewSite(fixtureApp(t), policy.Policy{}, 20001); err == nil {
		t.Fatal("missing policy accepted")
	}
}

func FuzzRender(f *testing.F) {
	for _, seed := range []string{"hello.example.com", "import.example.com", "{env.DOMAIN}", "x.example.com\n}\nimport /tmp/evil", `x.example.com"`, "x.example.com#", "import", "xn--x.example.com"} {
		f.Add(seed)
	}
	a, p := fixtureApp(f), fixturePolicy(f)
	f.Fuzz(func(t *testing.T, domain string) {
		input := a
		input.Domains = []spec.Domain{spec.Domain(domain)}
		s, err := NewSite(input, p, 20001)
		if err != nil {
			return
		}
		b, err := Render(s)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Count(b, []byte("{")) != 1 || bytes.Count(b, []byte("}")) != 1 || bytes.ContainsAny(b, "#\"\r") {
			t.Fatalf("unsafe block %q", b)
		}
		lines := strings.Split(string(b), "\n")
		if len(lines) != 6 || lines[0] != string(s.domains[0])+" {" || lines[1] != "\treverse_proxy 127.0.0.1:20001" || lines[2] != "\theader X-Content-Type-Options nosniff" || lines[3] != "\theader Referrer-Policy no-referrer" || lines[4] != "}" || lines[5] != "" {
			t.Fatalf("unexpected structure %q", b)
		}
	})
}

func TestRenderSingleDomainGolden(t *testing.T) {
	a := fixtureApp(t)
	a.Domains = []spec.Domain{"web.example.com"}
	site, err := NewSite(a, fixturePolicy(t), 20001)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Render(site)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/single.caddy")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRejectSymlinkedRoot(t *testing.T) {
	dir := t.TempDir()
	alias := dir + "/alias"
	if err := os.Symlink(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	if m, err := NewManager(alias, &fakeValidator{}, &fakeReloader{}); err == nil {
		m.Close()
		t.Fatal("symlinked root accepted")
	}
}

func FuzzSiteName(f *testing.F) {
	f.Add("hello", uint16(20001))
	f.Add("../outside", uint16(20002))
	f.Add("x\nimport", uint16(65535))
	a, p := fixtureApp(f), fixturePolicy(f)
	f.Fuzz(func(t *testing.T, name string, port uint16) {
		input := a
		input.Name = spec.Name(name)
		site, err := NewSite(input, p, spec.Port(port))
		if err != nil {
			return
		}
		if !appFile.MatchString(string(site.name) + ".caddy") {
			t.Fatal("unsafe app filename")
		}
		b, err := Render(site)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Count(b, []byte("{")) != 1 || bytes.Count(b, []byte("}")) != 1 {
			t.Fatal("unsafe block")
		}
	})
}

func TestSiteOwnsDomainSnapshot(t *testing.T) {
	a := fixtureApp(t)
	site, err := NewSite(a, fixturePolicy(t), 20001)
	if err != nil {
		t.Fatal(err)
	}
	a.Domains[0] = "malicious.example.com\n}\nimport /tmp/evil"
	got, err := Render(site)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/site.caddy")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("caller mutated validated site")
	}
}
