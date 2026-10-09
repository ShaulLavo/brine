package quadlet

import (
	"crypto/sha256"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func fixture(t testing.TB) (policy.Desired, plan.Plan) {
	t.Helper()
	a, err := spec.Parse([]byte(`schema_version=1
name="hello"
image="registry.example.com/team/web@sha256:` + strings.Repeat("a", 64) + `"
container_port=3000
domains=["web.example.com"]
`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.Parse([]byte(`schema_version=1
version="test-1"
allowed_domains=["*.example.com"]
persistent_roots=["/srv/brine/data"]
[[allowed_registries]]
host="registry.example.com"
repository_prefixes=["team"]
[resources]
memory_mb=256
pids_limit=64
[allowed_secrets]
hello=["token"]
`))
	if err != nil {
		t.Fatal(err)
	}
	d, err := policy.Normalize(a, p)
	if err != nil {
		t.Fatal(err)
	}
	return d, bind(t, d)
}
func bind(t testing.TB, d policy.Desired) plan.Plan {
	t.Helper()
	b, err := d.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	p := plan.Plan{Kind: plan.Create, App: string(d.Name), Hash: "sha256:" + strings.Repeat("b", 64), DesiredHash: fmt.Sprintf("sha256:%x", sha256.Sum256(b)), Image: target.Image{Digest: "sha256:" + strings.Repeat("a", 64), Platform: target.Platform{OS: "linux", Arch: "arm64"}}, HostPort: 20000}
	for _, s := range d.Secrets {
		p.Secrets = append(p.Secrets, plan.SecretBinding{Environment: s.Name, Reference: s.Reference, VersionName: "brine-hello-" + string(s.Reference) + "-v1", ID: "opaque-id"})
	}
	return p
}
func manifest() string { return "sha256:" + strings.Repeat("c", 64) }

func TestGolden(t *testing.T) {
	for _, name := range []string{"minimal", "full"} {
		t.Run(name, func(t *testing.T) {
			d, _ := fixture(t)
			if name == "full" {
				d.Environment = []policy.Environment{{Name: "Z", Value: "line one\nline two"}, {Name: "A", Value: `quote" slash\ %n $HOME café`}}
				d.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}}
			}
			u, err := Render(d, bind(t, d), manifest())
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile("testdata/" + name + ".container")
			if err != nil {
				t.Fatal(err)
			}
			if string(u.Bytes()) != string(want) {
				t.Fatalf("golden mismatch\ngot:\n%s\nwant:\n%s", u.Bytes(), want)
			}
			if u.Name() != "hello.container" {
				t.Fatal(u.Name())
			}
			slices.Reverse(d.Environment)
			v, err := Render(d, bind(t, d), manifest())
			if err != nil || string(u.Bytes()) != string(v.Bytes()) {
				t.Fatal("nondeterministic rendering", err)
			}
		})
	}
}

// parseEnvironment models Quadlet's C-unquoting followed by the generated
// ExecStart's systemd specifier and dollar expansion, not a shell parser.
func parseEnvironment(unit []byte) (map[string]string, error) {
	out := map[string]string{}
	for _, line := range strings.Split(string(unit), "\n") {
		if !strings.HasPrefix(line, "Environment=") {
			continue
		}
		text := strings.TrimPrefix(line, "Environment=")
		if len(text) < 2 || text[0] != '"' || text[len(text)-1] != '"' {
			return nil, fmt.Errorf("not one quoted assignment")
		}
		var b strings.Builder
		for i := 1; i < len(text)-1; i++ {
			c := text[i]
			if c == '"' {
				return nil, fmt.Errorf("unescaped quote")
			}
			if c == '\\' {
				i++
				if i >= len(text)-1 {
					return nil, fmt.Errorf("unfinished escape")
				}
				switch text[i] {
				case '\\', '"':
					c = text[i]
				case 'n':
					c = '\n'
				case 'r':
					c = '\r'
				case 't':
					c = '\t'
				default:
					return nil, fmt.Errorf("unknown escape")
				}
			}
			b.WriteByte(c)
		}
		decoded := b.String()
		var expanded strings.Builder
		for i := 0; i < len(decoded); i++ {
			if decoded[i] == '%' || decoded[i] == '$' {
				if i+1 >= len(decoded) || decoded[i+1] != decoded[i] {
					return nil, fmt.Errorf("expansion risk")
				}
				i++
			}
			expanded.WriteByte(decoded[i])
		}
		k, v, ok := strings.Cut(expanded.String(), "=")
		if !ok {
			return nil, fmt.Errorf("not assignment")
		}
		if _, exists := out[k]; exists {
			return nil, fmt.Errorf("duplicate assignment")
		}
		out[k] = v
	}
	return out, nil
}
func TestEnvironment(t *testing.T) {
	for _, value := range []string{"", "spaces in value", "\n[Service]\nExecStart=/bad", "a\r\nb\tc", `"'\\\n`, "%n %% %h", "$HOME $$ $ X", "café 世界", "# ; ="} {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			d, _ := fixture(t)
			d.Environment = []policy.Environment{{Name: "VALUE", Value: value}}
			u, err := Render(d, bind(t, d), manifest())
			if err != nil {
				t.Fatal(err)
			}
			env, err := parseEnvironment(u.Bytes())
			if err != nil || env["VALUE"] != value {
				t.Fatalf("round trip failed: %q %v", env, err)
			}
		})
	}
	for _, value := range []string{"\x00", "${HOME}", "\x01", "\x7f", "\u0085", " ", string([]byte{0xff})} {
		d, _ := fixture(t)
		d.Environment = []policy.Environment{{Name: "VALUE", Value: value}}
		if _, err := Render(d, bind(t, d), manifest()); err == nil {
			t.Fatalf("accepted unsafe value %q", value)
		}
	}
}
func TestRefuseBindings(t *testing.T) {
	tests := []struct {
		name   string
		change func(*policy.Desired, *plan.Plan, *string)
	}{
		{"stale desired", func(d *policy.Desired, p *plan.Plan, m *string) {
			d.Environment = []policy.Environment{{Name: "A", Value: "changed"}}
		}},
		{"plan hash", func(d *policy.Desired, p *plan.Plan, m *string) { p.Hash = "bad\n[Service]" }},
		{"manifest", func(d *policy.Desired, p *plan.Plan, m *string) { *m = "latest" }},
		{"image", func(d *policy.Desired, p *plan.Plan, m *string) { p.Image.Digest = manifest() }},
		{"platform", func(d *policy.Desired, p *plan.Plan, m *string) { p.Image.Platform.Arch = "arm64\nExecStart=x" }},
		{"port", func(d *policy.Desired, p *plan.Plan, m *string) { p.HostPort = 80 }},
		{"port overflow", func(d *policy.Desired, p *plan.Plan, m *string) { p.HostPort = 65536 }},
		{"app", func(d *policy.Desired, p *plan.Plan, m *string) { p.App = "other" }},
		{"conflict", func(d *policy.Desired, p *plan.Plan, m *string) { p.Kind = plan.Conflict }},
		{"duplicate env", func(d *policy.Desired, p *plan.Plan, m *string) {
			d.Environment = []policy.Environment{{Name: "A"}, {Name: "A"}}
			*p = bind(t, *d)
		}},
		{"env key", func(d *policy.Desired, p *plan.Plan, m *string) {
			d.Environment = []policy.Environment{{Name: "A\nExecStart"}}
			*p = bind(t, *d)
		}},
		{"secret missing", func(d *policy.Desired, p *plan.Plan, m *string) {
			d.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}}
			*p = bind(t, *d)
			p.Secrets = nil
		}},
		{"secret injection", func(d *policy.Desired, p *plan.Plan, m *string) {
			d.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}}
			*p = bind(t, *d)
			p.Secrets[0].VersionName = "brine-hello-token-v1,type=mount"
		}},
		{"name injection", func(d *policy.Desired, p *plan.Plan, m *string) { d.Name = "../bad"; *p = bind(t, *d) }},
		{"resource zero", func(d *policy.Desired, p *plan.Plan, m *string) { d.Resources.MemoryMB = 0; *p = bind(t, *d) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, p := fixture(t)
			m := manifest()
			tc.change(&d, &p, &m)
			if _, err := Render(d, p, m); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
}
func FuzzEnvironment(f *testing.F) {
	for _, s := range []string{"", "line\nquote\"\\ %n $HOME", "${x}", "世界", "\x00", "\x01"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, value string) {
		d, _ := fixture(t)
		d.Environment = []policy.Environment{{Name: "VALUE", Value: value}}
		u, err := Render(d, bind(t, d), manifest())
		if err != nil {
			return
		}
		env, err := parseEnvironment(u.Bytes())
		if err != nil || env["VALUE"] != value {
			t.Fatal("environment did not round trip")
		}
	})
}

func TestRenderUsesVerifiedDesiredNotRedactedPlanPayload(t *testing.T) {
	d, _ := fixture(t)
	d.Environment = []policy.Environment{{Name: "VALUE", Value: "runtime value"}}
	raw, err := os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Build(plan.Input{Desired: d, Snapshot: snapshot, Image: target.Image{Digest: "sha256:" + strings.Repeat("a", 64), Platform: target.Platform{OS: "linux", Arch: "arm64"}}, State: plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{}}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != plan.Create {
		t.Fatal("fixture plan is not deployable intent", p.Kind)
	}
	u, err := Render(d, p, manifest())
	if err != nil {
		t.Fatal(err)
	}
	values, err := parseEnvironment(u.Bytes())
	if err != nil || values["VALUE"] != "runtime value" {
		t.Fatal("lost runtime value", err)
	}
	for _, change := range p.Changes {
		if change.Quadlet != nil {
			if _, err = Render(change.Quadlet.Desired, p, manifest()); err == nil {
				t.Fatal("accepted plan payload without env values")
			}
			return
		}
	}
	t.Fatal("missing Quadlet change")
}

func TestRetainedPortSurvivesPolicyRangeChange(t *testing.T) {
	d, p := fixture(t)
	p.Kind = plan.Update
	p.HostPort = 30000
	if _, err := Render(d, p, manifest()); err != nil {
		t.Fatal("refused the planner's retained stable port", err)
	}
	p.Changes = []plan.Change{{Kind: plan.AllocatePort, Allocation: &plan.PortAllocation{App: p.App, Port: p.HostPort}}}
	if _, err := Render(d, p, manifest()); err == nil {
		t.Fatal("accepted new allocation outside policy")
	}
}
