package policy

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/spec"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/operator.toml")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func app(t *testing.T) spec.App {
	t.Helper()
	a, e := spec.Parse([]byte(`schema_version=1
name="hello"
image="registry.example.com:5000/team/web@sha256:` + strings.Repeat("a", 64) + `"
container_port=3000
domains=["web.example.com"]
[environment]
Z="last"
A="first"
[secrets]
TOKEN="hello-token"
`))
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func TestEnforce(t *testing.T) {
	p, e := Parse(fixture(t))
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name   string
		change func(*spec.App)
		code   string
	}{
		{"allowed", func(a *spec.App) {}, ""},
		{"nested wildcard", func(a *spec.App) { a.Domains = []spec.Domain{"nested.web.example.com"} }, ""},
		{"domain case", func(a *spec.App) { a.Domains = []spec.Domain{"WEB.EXAMPLE.COM"} }, ""},
		{"suffix boundary", func(a *spec.App) { a.Domains = []spec.Domain{"evilexample.com"} }, "policy.domain_denied"},
		{"wildcard apex", func(a *spec.App) { a.Domains = []spec.Domain{"example.com"} }, "policy.domain_denied"},
		{"exact domain", func(a *spec.App) { a.Domains = []spec.Domain{"other.example.net"} }, ""},
		{"exact not suffix", func(a *spec.App) { a.Domains = []spec.Domain{"sub.other.example.net"} }, "policy.domain_denied"},
		{"IDN unicode", func(a *spec.App) { a.Domains = []spec.Domain{"é.example.com"} }, "policy.invalid_spec"},
		{"IDN ascii", func(a *spec.App) { a.Domains = []spec.Domain{"xn--caf-dma.example.com"} }, "policy.invalid_spec"},
		{"registry different port", func(a *spec.App) {
			a.Image = spec.ImageReference(strings.Replace(string(a.Image), ":5000", ":5001", 1))
		}, "policy.registry_denied"},
		{"registry no port", func(a *spec.App) { a.Image = spec.ImageReference(strings.Replace(string(a.Image), ":5000", "", 1)) }, "policy.registry_denied"},
		{"registry prefix", func(a *spec.App) {
			a.Image = spec.ImageReference(strings.Replace(string(a.Image), "team/web", "teamevil/web", 1))
		}, "policy.registry_denied"},
		{"registry host boundary", func(a *spec.App) {
			a.Image = spec.ImageReference(strings.Replace(string(a.Image), "registry.example.com", "evilregistry.example.com", 1))
		}, "policy.registry_denied"},
		{"registry case invalid spec", func(a *spec.App) {
			a.Image = spec.ImageReference(strings.Replace(string(a.Image), "registry", "REGISTRY", 1))
		}, "policy.invalid_spec"},
		{"memory ceiling", func(a *spec.App) { a.Resources = &spec.Resources{MemoryMB: 513, PIDsLimit: 128} }, "policy.resources_denied"},
		{"pid ceiling", func(a *spec.App) { a.Resources = &spec.Resources{MemoryMB: 512, PIDsLimit: 129} }, "policy.resources_denied"},
		{"ceiling inclusive", func(a *spec.App) { a.Resources = &spec.Resources{MemoryMB: 512, PIDsLimit: 128} }, ""},
		{"secret denied", func(a *spec.App) { a.Secrets["TOKEN"] = "do-not-echo" }, "policy.secret_denied"},
		{"other app secrets", func(a *spec.App) { a.Name = "other" }, "policy.secret_denied"},
		{"expansion", func(a *spec.App) { a.Environment["A"] = "${DO_NOT_ECHO}" }, "policy.invalid_spec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := app(t)
			tc.change(&a)
			d, e := Normalize(a, p)
			if tc.code == "" {
				if e != nil {
					t.Fatal(e)
				}
				if d.Resources.MemoryMB != 512 || d.Resources.PIDsLimit != 128 {
					t.Fatal("defaults not explicit")
				}
				return
			}
			var r *Refusal
			if !errors.As(e, &r) || r.Code != tc.code || r.Category() != 4 {
				t.Fatalf("unexpected refusal %v", e)
			}
			if !reflect.DeepEqual(d, Desired{}) {
				t.Fatal("refusal returned desired")
			}
			if strings.Contains(e.Error(), "do-not-echo") || strings.Contains(e.Error(), "DO_NOT_ECHO") {
				t.Fatal("leaked input")
			}
		})
	}
}
func TestDecode(t *testing.T) {
	b := fixture(t)
	for _, tc := range []struct {
		name string
		b    []byte
	}{
		{"unknown", append(bytes.Clone(b), []byte("\nunknown_secret = 'do-not-echo'\n")...)},
		{"case alias", bytes.Replace(b, []byte("schema_version"), []byte("Schema_version"), 1)},
		{"duplicate", append(bytes.Clone(b), []byte("\n[resources]\nmemory_mb=512\n")...)},
		{"version", bytes.Replace(b, []byte("schema_version = 1"), []byte("schema_version = 2"), 1)},
		{"port inverted", bytes.Replace(b, []byte("# ports"), []byte("[app_ports]\nmin=21000\nmax=20000"), 1)},
		{"privileged port", bytes.Replace(b, []byte("# ports"), []byte("[app_ports]\nmin=80\nmax=90"), 1)},
		{"IDN", bytes.Replace(b, []byte("*.Example.com"), []byte("*.xn--caf-dma.com"), 1)},
		{"Unicode folds to ASCII", bytes.Replace(b, []byte("*.Example.com"), []byte("*.K.example.com"), 1)},
		{"registry Unicode folds to ASCII", bytes.Replace(b, []byte("Registry.Example.com:5000"), []byte("K.example.com:5000"), 1)},
		{"registry path", bytes.Replace(b, []byte("Registry.Example.com:5000"), []byte("registry.example.com/team"), 1)},
		{"port overflow", bytes.Replace(b, []byte("# ports"), []byte("[app_ports]\nmin=20000\nmax=65536"), 1)},
		{"missing resource ceiling", bytes.Replace(b, []byte("memory_mb = 512"), nil, 1)},
		{"missing revision", bytes.Replace(b, []byte("version = \"operator-1\""), nil, 1)},
		{"secret invalid", bytes.Replace(b, []byte("hello-token"), []byte("do-not-echo/value"), 1)},
		{"registry case alias", bytes.Replace(b, []byte("host ="), []byte("Host ="), 1)},
		{"registry duplicate", append(bytes.Clone(b), []byte("\n[[allowed_registries]]\nhost='registry.example.com:5000'\n")...)},
		{"wildcard malformed", bytes.Replace(b, []byte("*.Example.com"), []byte("*Example.com"), 1)},
		{"root control", bytes.Replace(b, []byte("/srv/brine/data"), []byte("/srv/brine/\\n"), 1)},
		{"root backslash", bytes.Replace(b, []byte("/srv/brine/data"), []byte("/srv/brine/\\\\data"), 1)},
		{"root traversal", bytes.Replace(b, []byte("/srv/brine/data"), []byte("/srv/../data"), 1)},
		{"root relative", bytes.Replace(b, []byte("/srv/brine/data"), []byte("data"), 1)},
		{"root slash", bytes.Replace(b, []byte("/srv/brine/data"), []byte("/"), 1)},
		{"resource zero", bytes.Replace(b, []byte("memory_mb = 512"), []byte("memory_mb = 0"), 1)},
		{"too large", bytes.Repeat([]byte("x"), (1<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, e := Parse(tc.b)
			var r *Refusal
			if !errors.As(e, &r) || !reflect.DeepEqual(p, Policy{}) {
				t.Fatalf("not refused safely %v", e)
			}
			if strings.Contains(e.Error(), "do-not-echo") {
				t.Fatal("leaked input")
			}
		})
	}
	if _, e := Normalize(app(t), Policy{}); e == nil {
		t.Fatal("missing policy accepted")
	}
}
func TestDeterminism(t *testing.T) {
	p, e := Parse(fixture(t))
	if e != nil {
		t.Fatal(e)
	}
	a := app(t)
	a.Domains = []spec.Domain{"z.example.com", "A.EXAMPLE.COM"}
	first, e := Normalize(a, p)
	if e != nil {
		t.Fatal(e)
	}
	want, e := first.CanonicalBytes()
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 100; i++ {
		a.Domains = []spec.Domain{"a.example.com", "Z.EXAMPLE.COM"}
		a.Environment = map[string]string{"A": "first", "Z": "last"}
		got, e := Normalize(a, p)
		if e != nil {
			t.Fatal(e)
		}
		b, e := got.CanonicalBytes()
		if e != nil || !bytes.Equal(want, b) {
			t.Fatal("unstable canonical bytes")
		}
	}
	if first.AppPorts.Min != 20000 || first.AppPorts.Max != 20999 {
		t.Fatal("port defaults")
	}
	changed := bytes.Replace(fixture(t), []byte("*.Example.com\", \"other.example.net"), []byte("other.example.net\", \"*.example.com"), 1)
	q, e := Parse(changed)
	if e != nil {
		t.Fatal(e)
	}
	if p.Hash() != q.Hash() {
		t.Fatal("policy order changes hash")
	}
	a.Environment["A"] = "changed"
	b, _ := first.CanonicalBytes()
	if !bytes.Equal(want, b) {
		t.Fatal("input aliases desired")
	}
}
func FuzzParse(f *testing.F) {
	b, _ := os.ReadFile("testdata/operator.toml")
	f.Add(b)
	f.Add([]byte("schema_version=1"))
	f.Fuzz(func(t *testing.T, b []byte) {
		p, e := Parse(b)
		if e != nil {
			var r *Refusal
			if !errors.As(e, &r) || !reflect.DeepEqual(p, Policy{}) {
				t.Fatal("unsafe decode error")
			}
			return
		}
		c, e := p.CanonicalBytes()
		if e != nil || len(c) == 0 || p.Hash() == "" {
			t.Fatal("invalid accepted policy")
		}
	})
}

func TestPolicyOptions(t *testing.T) {
	for _, tc := range []struct {
		name, from, to string
		accept         bool
	}{
		{"all repositories", "repository_prefixes = [\"team\"]", "repository_prefixes = []", true},
		{"exact repository", "repository_prefixes = [\"team\"]", "repository_prefixes = [\"team/web\"]", true},
		{"different repository", "repository_prefixes = [\"team\"]", "repository_prefixes = [\"team/other\"]", false},
		{"no registries", "host = \"Registry.Example.com:5000\"", "host = \"other.example.com:5000\"", false},
		{"empty domains", "allowed_domains = [\"*.Example.com\", \"other.example.net\"]", "allowed_domains = []", false},
		{"secret case is significant", "hello-token", "HELLO-TOKEN", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, e := Parse(bytes.Replace(fixture(t), []byte(tc.from), []byte(tc.to), 1))
			if e != nil {
				t.Fatal(e)
			}
			_, e = Normalize(app(t), p)
			if (e == nil) != tc.accept {
				t.Fatalf("unexpected result %v", e)
			}
		})
	}
	b := bytes.Replace(fixture(t), []byte("# ports"), []byte("[app_ports]\nmin=30000\nmax=30000"), 1)
	p, e := Parse(b)
	if e != nil {
		t.Fatal(e)
	}
	a := app(t)
	a.Resources = &spec.Resources{MemoryMB: 64, PIDsLimit: 8}
	d, e := Normalize(a, p)
	if e != nil {
		t.Fatal(e)
	}
	if d.AppPorts.Min != 30000 || d.AppPorts.Max != 30000 || d.Resources.MemoryMB != 64 || d.Resources.PIDsLimit != 8 || d.Health.Path != "/" || d.Health.ExpectedStatus != 200 || d.Health.StartupDeadlineSeconds != 30 || d.Health.TimeoutSeconds != 3 {
		t.Fatal("resolved settings incorrect")
	}
	roots := p.PersistentRoots()
	roots[0] = "/changed"
	if p.PersistentRoots()[0] != "/srv/brine/data" {
		t.Fatal("policy accessor aliases immutable policy")
	}
	changed, e := Parse(bytes.Replace(b, []byte("/srv/brine/data"), []byte("/srv/brine/other"), 1))
	if e != nil {
		t.Fatal(e)
	}
	if p.Hash() == changed.Hash() {
		t.Fatal("roots absent from hash")
	}
	zero := Policy{}
	if _, e := zero.CanonicalBytes(); e == nil || zero.Version() != "" || zero.Hash() != "" || zero.PersistentRoots() != nil || zero.AppPorts() != (PortRange{}) {
		t.Fatal("zero policy has authority")
	}
}

func TestCanonicalCollections(t *testing.T) {
	p, e := Parse(bytes.Replace(fixture(t), []byte("hello = [\"hello-token\"]"), []byte("hello = [\"hello-token\", \"second-token\"]"), 1))
	if e != nil {
		t.Fatal(e)
	}
	a := app(t)
	a.Domains = []spec.Domain{"z.example.com", "a.example.com"}
	a.Secrets["ANOTHER"] = "second-token"
	d, e := Normalize(a, p)
	if e != nil {
		t.Fatal(e)
	}
	want, _ := d.CanonicalBytes()
	if d.Environment[0].Name != "A" || d.Secrets[0].Name != "ANOTHER" || d.Domains[0] != "a.example.com" {
		t.Fatal("model not canonically ordered")
	}
	slices.Reverse(d.Domains)
	slices.Reverse(d.Environment)
	slices.Reverse(d.Secrets)
	got, _ := d.CanonicalBytes()
	if !bytes.Equal(want, got) {
		t.Fatal("collection shuffle changes bytes")
	}
	a.Secrets["ANOTHER"] = "hello-token"
	a.Domains[0] = "other.example.net"
	a.Resources = &spec.Resources{MemoryMB: 1, PIDsLimit: 1}
	got, _ = d.CanonicalBytes()
	if !bytes.Equal(want, got) {
		t.Fatal("input aliases normalized model")
	}
	b := bytes.Replace(fixture(t), []byte("repository_prefixes = [\"team\"]"), []byte("repository_prefixes = [\"team\",\"extra\"]"), 1)
	q, e := Parse(b)
	if e != nil {
		t.Fatal(e)
	}
	r, e := Parse(bytes.Replace(b, []byte("[\"team\",\"extra\"]"), []byte("[\"extra\",\"team\",\"team\"]"), 1))
	if e != nil {
		t.Fatal(e)
	}
	if q.Hash() != r.Hash() {
		t.Fatal("set ordering or duplication changes policy hash")
	}
}
