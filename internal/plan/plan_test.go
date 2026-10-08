package plan

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func fixture(t testing.TB, name string) Input {
	t.Helper()
	read := func(path string) []byte {
		b, e := os.ReadFile(path)
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
	// Reuse the strict spec and operator fixtures with a matching allowed repository.
	a, e := spec.Parse(bytes.ReplaceAll(read("../spec/testdata/valid-minimal.toml"), []byte("example/hello"), []byte("team/hello")))
	if e != nil {
		t.Fatal(e)
	}
	d, e := policy.Normalize(a, p)
	if e != nil {
		t.Fatal(e)
	}
	return Input{Desired: d, Snapshot: s, Image: target.Image{Digest: strings.Split(string(d.Image), "@")[1], Platform: target.Platform{OS: "linux", Arch: s.Arch}}, Evidence: Evidence{Routes: target.Known([]Route{}), Listeners: target.Known([]Listener{}), Applied: target.Known([]Applied{})}}
}

func build(t testing.TB, in Input) Plan {
	t.Helper()
	p, e := Build(in)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func installed(t testing.TB) Input {
	in := fixture(t, "one-app")
	p := build(t, in)
	app := &(*in.Snapshot.Apps.Value)[0]
	in.Evidence.Routes = target.Known([]Route{{Domain: in.Desired.Domains[0], App: "hello"}})
	in.Evidence.Listeners = target.Known([]Listener{{Port: 20000, App: "hello"}})
	in.Evidence.Applied = target.Known([]Applied{{App: "hello", ConfigHash: p.ConfigHash, Units: append([]target.Unit{}, (*app.QuadletUnits.Value)...), CaddyHash: in.Snapshot.CaddyConfig.Value.Files[0].Hash}})
	return in
}

func TestFixtureMatrix(t *testing.T) {
	tests := []struct {
		name     string
		input    func(testing.TB) Input
		change   func(*Input)
		kind     Kind
		conflict ConflictCode
	}{
		{"create", func(t testing.TB) Input { return fixture(t, "fresh-arm64") }, nil, Create, ""},
		{"skip occupied", func(t testing.TB) Input { return fixture(t, "port-conflict") }, nil, Create, ""},
		{"no-op", installed, nil, NoOp, ""},
		{"image update", installed, func(i *Input) {
			i.Desired.Image = spec.ImageReference(strings.ReplaceAll(string(i.Desired.Image), strings.Repeat("a", 64), strings.Repeat("e", 64)))
			i.Image.Digest = "sha256:" + strings.Repeat("e", 64)
		}, Update, ""},
		{"domain update", installed, func(i *Input) { i.Desired.Domains = []spec.Domain{"other.example.net"} }, Update, ""},
		{"env update", installed, func(i *Input) { i.Desired.Environment = []policy.Environment{{Name: "APP_ENV", Value: "production"}} }, Update, ""},
		{"domain conflict", installed, func(i *Input) {
			i.Evidence.Routes = target.Known([]Route{{Domain: i.Desired.Domains[0], App: "other"}})
		}, Conflict, DomainOwned},
		{"foreign route", installed, func(i *Input) { i.Evidence.Routes = target.Known([]Route{{Domain: i.Desired.Domains[0]}}) }, Conflict, DomainOwned},
		{"port conflict", installed, func(i *Input) { i.Evidence.Listeners = target.Known([]Listener{{Port: 20000, App: "other"}}) }, Conflict, PortOwned},
		{"unattributed port", installed, func(i *Input) { i.Evidence.Listeners = target.Known([]Listener{}) }, Conflict, PortOwned},
		{"unsupported", func(t testing.TB) Input { return fixture(t, "unsupported-ubuntu") }, nil, Conflict, UnsupportedTarget},
		{"platform", installed, func(i *Input) { i.Image.Platform.Arch = "amd64" }, Conflict, ImagePlatform},
		{"missing secret", installed, func(i *Input) { i.Desired.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "missing"}} }, Conflict, SecretMissing},
		{"unknown ports", installed, func(i *Input) { i.Snapshot.UsedPorts = target.Observation[[]target.Port]{Status: target.Unknown} }, Conflict, UnknownFacts},
		{"unknown generation", installed, func(i *Input) { i.Snapshot.Generation = target.Observation[uint64]{Status: target.Unknown} }, Conflict, UnknownFacts},
		{"unknown routes", installed, func(i *Input) { i.Evidence.Routes = target.Observation[[]Route]{Status: target.Unknown} }, Conflict, UnknownFacts},
		{"unsupported observation", installed, func(i *Input) { i.Snapshot.UsedPorts = target.Observation[[]target.Port]{Status: target.Unsupported} }, Conflict, UnsupportedTarget},
		{"port exhaustion", func(t testing.TB) Input { return fixture(t, "port-conflict") }, func(i *Input) { i.Desired.AppPorts = policy.PortRange{Min: 20000, Max: 20000} }, Conflict, PortsExhausted},
		{"artifact drift", installed, func(i *Input) {
			(*(*i.Snapshot.Apps.Value)[0].QuadletUnits.Value)[0].Hash = "sha256:" + strings.Repeat("f", 64)
		}, Conflict, ArtifactDrift},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.input(t)
			if tt.change != nil {
				tt.change(&in)
			}
			p := build(t, in)
			if p.Kind != tt.kind {
				t.Fatalf("kind %s want %s: %+v", p.Kind, tt.kind, p.Conflicts)
			}
			if tt.conflict != "" {
				if len(p.Conflicts) == 0 || p.Conflicts[0].Code != tt.conflict {
					t.Fatalf("conflicts %+v", p.Conflicts)
				}
				if len(p.Changes) != 0 {
					t.Fatal("conflict has changes")
				}
			}
			if tt.name == "skip occupied" && p.HostPort != 20001 {
				t.Fatal(p.HostPort)
			}
			if p.Target != in.Snapshot.Identity || !reflect.DeepEqual(p.ObservedGeneration, in.Snapshot.Generation) {
				t.Fatal("lost staleness precondition")
			}
		})
	}
}

func TestSecretVersions(t *testing.T) {
	in := fixture(t, "one-app")
	in.Desired.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}}
	app := &(*in.Snapshot.Apps.Value)[0]
	*app.Secrets.Value = append(*app.Secrets.Value, target.Secret{Name: "brine-hello-token-v10", ID: "synthetic-10"}, target.Secret{Name: "brine-other-token-v99", ID: "synthetic-99"})
	p := build(t, in)
	if len(p.Secrets) != 1 || p.Secrets[0].VersionName != "brine-hello-token-v10" {
		t.Fatal(p.Secrets)
	}
	if p.Secrets[0].ID != "synthetic-10" {
		t.Fatal(p.Secrets)
	}
}

func TestDeterminismAndPurity(t *testing.T) {
	in := installed(t)
	in.Desired.Environment = []policy.Environment{{Name: "B", Value: "b"}, {Name: "A", Value: "a"}}
	in.Desired.Domains = append(in.Desired.Domains, "other.example.net")
	in.Snapshot.UsedPorts = target.Known([]target.Port{20002, 20000})
	in.Evidence.Routes = target.Known([]Route{{Domain: "other.example.net", App: "hello"}, {Domain: "hello.example.com", App: "hello"}})
	before, _ := json.Marshal(in)
	p := build(t, in)
	after, _ := json.Marshal(in)
	if !bytes.Equal(before, after) {
		t.Fatal("mutated input")
	}
	reverse := func() {
		in.Desired.Domains[0], in.Desired.Domains[1] = in.Desired.Domains[1], in.Desired.Domains[0]
		in.Desired.Environment[0], in.Desired.Environment[1] = in.Desired.Environment[1], in.Desired.Environment[0]
		(*in.Snapshot.UsedPorts.Value)[0], (*in.Snapshot.UsedPorts.Value)[1] = (*in.Snapshot.UsedPorts.Value)[1], (*in.Snapshot.UsedPorts.Value)[0]
		(*in.Evidence.Routes.Value)[0], (*in.Evidence.Routes.Value)[1] = (*in.Evidence.Routes.Value)[1], (*in.Evidence.Routes.Value)[0]
		u := (*in.Snapshot.Apps.Value)[0].QuadletUnits.Value
		(*u)[0], (*u)[1] = (*u)[1], (*u)[0]
	}
	reverse()
	q := build(t, in)
	if !reflect.DeepEqual(p, q) {
		t.Fatalf("plans differ\n%+v\n%+v", p, q)
	}
	// Output owns its observations and slices, not pointers into caller memory.
	*in.Snapshot.Generation.Value = 999
	if *p.ObservedGeneration.Value == 999 {
		t.Fatal("aliased generation")
	}
}

func TestHashPreconditions(t *testing.T) {
	in := installed(t)
	p := build(t, in)
	tests := []func(*Input){func(i *Input) { i.Snapshot.Generation = target.Known(uint64(5)) }, func(i *Input) { i.Snapshot.Identity.ID = "other-target" }, func(i *Input) { i.Desired.PolicyVersion = "operator-2" }, func(i *Input) { i.Desired.PolicyHash = "sha256:" + strings.Repeat("f", 64) }, func(i *Input) { i.Snapshot.CaddyConfig.Value.Files[0].Hash = "sha256:" + strings.Repeat("f", 64) }}
	for _, change := range tests {
		next := installed(t)
		change(&next)
		if build(t, next).Hash == p.Hash {
			t.Fatal("precondition not hashed")
		}
	}
}

func goldenInputs(t testing.TB) map[string]Input {
	conflict := installed(t)
	conflict.Evidence.Routes = target.Known([]Route{{Domain: conflict.Desired.Domains[0], App: "other"}})
	return map[string]Input{"create": fixture(t, "fresh-arm64"), "no-op": installed(t), "conflict": conflict}
}
func TestGolden(t *testing.T) {
	for name, in := range goldenInputs(t) {
		t.Run(name, func(t *testing.T) {
			p := build(t, in)
			b, e := p.CanonicalBytes()
			if e != nil {
				t.Fatal(e)
			}
			want, e := os.ReadFile("testdata/" + name + ".json")
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(b, want) {
				t.Fatalf("golden mismatch\n%s", b)
			}
		})
	}
}

func FuzzHashEquality(f *testing.F) {
	f.Add(uint16(7), "blue", true)
	f.Add(uint16(19), "green", false)
	f.Fuzz(func(t *testing.T, n uint16, value string, shuffle bool) {
		if len(value) > 100 {
			t.Skip()
		}
		in := fixture(t, "fresh-arm64")
		in.Snapshot.Generation = target.Known(uint64(n))
		in.Desired.Environment = []policy.Environment{{Name: "A", Value: value}, {Name: "B", Value: "fixed"}}
		p := build(t, in)
		if shuffle {
			in.Desired.Environment[0], in.Desired.Environment[1] = in.Desired.Environment[1], in.Desired.Environment[0]
		}
		q := build(t, in)
		if p.Hash == q.Hash && !reflect.DeepEqual(p, q) {
			t.Fatal("equal hashes, unequal plans")
		}
		if !reflect.DeepEqual(p, q) {
			t.Fatal("ordering changed plan")
		}
		in.Desired.Environment[0].Value += "changed"
		r := build(t, in)
		if p.Hash == r.Hash && !reflect.DeepEqual(p, r) {
			t.Fatal("equal hashes, unequal generated plans")
		}
	})
}

func TestChangeOrderAndPreservedCaddy(t *testing.T) {
	in := installed(t)
	in.Snapshot.CaddyConfig.Value.Files = append(in.Snapshot.CaddyConfig.Value.Files, target.CaddyFile{Name: "unrelated.caddy", Hash: "sha256:" + strings.Repeat("f", 64)})
	in.Desired.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}}
	p := build(t, in)
	if p.Kind != Update || p.HostPort != 20000 {
		t.Fatalf("%s port %d", p.Kind, p.HostPort)
	}
	kinds := []ChangeKind{}
	for _, c := range p.Changes {
		kinds = append(kinds, c.Kind)
	}
	want := []ChangeKind{PullImage, BindSecret, RenderQuadlet, StageCaddy, RestartApp}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatal(kinds)
	}
	caddy := p.Changes[3].Caddy
	if caddy.Previous != 2 || caddy.Next != 3 || len(caddy.Preserve) != 1 || caddy.Preserve[0].Name != "unrelated.caddy" {
		t.Fatal(caddy)
	}

}

func TestReservedPortsAndPrecreatedSecrets(t *testing.T) {
	in := fixture(t, "one-app")
	app := &(*in.Snapshot.Apps.Value)[0]
	app.CurrentRelease = target.Observation[string]{Status: target.Absent}
	app.AllocatedHostPort = target.Observation[target.Port]{Status: target.Absent}
	in.Snapshot.UsedPorts = target.Known([]target.Port{})
	in.Desired.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}}
	p := build(t, in)
	if p.Kind != Create || p.HostPort != 20000 || len(p.Secrets) != 1 {
		t.Fatalf("%+v", p)
	}
	other := *app
	other.Name = "other"
	other.AllocatedHostPort = target.Known(target.Port(20000))
	*in.Snapshot.Apps.Value = append(*in.Snapshot.Apps.Value, other)
	p = build(t, in)
	if p.HostPort != 20001 {
		t.Fatal(p.HostPort)
	}
}

func TestMalformedEvidenceAndPinnedMetadata(t *testing.T) {
	for _, change := range []func(*Input){
		func(i *Input) { i.Image.Digest = "sha256:" + strings.Repeat("f", 64) },
		func(i *Input) { i.Evidence.Routes = target.Known[[]Route](nil) },
		func(i *Input) {
			i.Evidence.Routes = target.Observation[[]Route]{Status: target.Unknown, Value: new([]Route)}
		},
		func(i *Input) { i.Evidence.Listeners = target.Known([]Listener{{Port: 0}}) },
	} {
		in := fixture(t, "fresh-arm64")
		change(&in)
		p, e := Build(in)
		if e == nil || !reflect.DeepEqual(p, Plan{}) {
			t.Fatalf("expected zero plan and error, got %+v, %v", p, e)
		}
	}
}

func TestSecretRotationUpdates(t *testing.T) {
	in := installed(t)
	in.Desired.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}}
	initial := build(t, in)
	(*in.Evidence.Applied.Value)[0].ConfigHash = initial.ConfigHash
	if p := build(t, in); p.Kind != NoOp {
		t.Fatal(p.Kind)
	}
	app := &(*in.Snapshot.Apps.Value)[0]
	*app.Secrets.Value = append(*app.Secrets.Value, target.Secret{Name: "brine-hello-token-v3", ID: "synthetic-3"})
	p := build(t, in)
	if p.Kind != Update || p.Secrets[0].VersionName != "brine-hello-token-v3" || p.Hash == initial.Hash {
		t.Fatal(p)
	}
}

func TestAllSetPermutations(t *testing.T) {
	in := installed(t)
	in.Desired.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}, {Name: "KEY", Reference: "key"}}
	in.Snapshot.CaddyConfig.Value.Files = append(in.Snapshot.CaddyConfig.Value.Files, target.CaddyFile{Name: "other.caddy", Hash: "sha256:" + strings.Repeat("e", 64)})
	other := (*in.Snapshot.Apps.Value)[0]
	other.Name = "other"
	other.Secrets = target.Known(append([]target.Secret{}, (*other.Secrets.Value)...))
	other.AllocatedHostPort = target.Known(target.Port(20001))
	*in.Snapshot.Apps.Value = append(*in.Snapshot.Apps.Value, other)
	*in.Evidence.Applied.Value = append(*in.Evidence.Applied.Value, Applied{App: "other", ConfigHash: "sha256:" + strings.Repeat("e", 64), Units: []target.Unit{}})
	in.Evidence.Listeners = target.Known([]Listener{{Port: 20000, App: "hello"}, {Port: 20001, App: "other"}})
	p := build(t, in)
	in.Desired.Secrets[0], in.Desired.Secrets[1] = in.Desired.Secrets[1], in.Desired.Secrets[0]
	apps := in.Snapshot.Apps.Value
	(*apps)[0], (*apps)[1] = (*apps)[1], (*apps)[0]
	for _, a := range *apps {
		sec := a.Secrets.Value
		(*sec)[0], (*sec)[1] = (*sec)[1], (*sec)[0]
	}
	files := in.Snapshot.CaddyConfig.Value.Files
	files[0], files[1] = files[1], files[0]
	applied := in.Evidence.Applied.Value
	(*applied)[0], (*applied)[1] = (*applied)[1], (*applied)[0]
	units := (*applied)[1].Units
	units[0], units[1] = units[1], units[0]
	listeners := in.Evidence.Listeners.Value
	(*listeners)[0], (*listeners)[1] = (*listeners)[1], (*listeners)[0]
	q := build(t, in)
	if !reflect.DeepEqual(p, q) {
		t.Fatal("set permutations changed hash or plan")
	}
}

func TestKeptPortOutsideNewRange(t *testing.T) {
	in := installed(t)
	in.Desired.AppPorts = policy.PortRange{Min: 21000, Max: 21001}
	p := build(t, in)
	if p.Kind != Update || p.HostPort != 20000 {
		t.Fatal(p.Kind, p.HostPort)
	}
}

func TestNoOpRequiresOwnedArtifacts(t *testing.T) {
	in := installed(t)
	*(*in.Snapshot.Apps.Value)[0].QuadletUnits.Value = []target.Unit{}
	(*in.Evidence.Applied.Value)[0].Units = []target.Unit{}
	p := build(t, in)
	if p.Kind == NoOp {
		t.Fatal("missing container unit is not a no-op")
	}
	in = installed(t)
	in.Snapshot.CaddyConfig = target.Observation[target.CaddyConfigSet]{Status: target.Absent}
	(*in.Evidence.Applied.Value)[0].CaddyHash = ""
	p = build(t, in)
	if p.Kind == NoOp {
		t.Fatal("missing app route file is not a no-op")
	}
}
