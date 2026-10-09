package plan

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
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
	a, e := spec.Parse(bytes.ReplaceAll(read("../spec/testdata/valid-minimal.toml"), []byte("example/hello"), []byte("team/hello")))
	if e != nil {
		t.Fatal(e)
	}
	d, e := policy.Normalize(a, p)
	if e != nil {
		t.Fatal(e)
	}
	image := Image{ManifestDigest: target.Observation[string]{Status: target.Unknown}, Digest: strings.Split(string(d.Image), "@")[1], Platform: target.Platform{OS: "linux", Arch: s.Arch}}
	state := BrineState{Target: s.Identity, Generation: *s.Generation.Value, Releases: []CurrentRelease{}}
	for _, app := range *s.Apps.Value {
		if app.Name == "hello" && app.Image.Status == target.KnownStatus {
			state.Releases = append(state.Releases, CurrentRelease{App: "hello", ID: "release-0001", Desired: d, Image: Image{Digest: app.Image.Value.Digest, Platform: app.Image.Value.Platform, ManifestDigest: target.Observation[string]{Status: target.Unknown}}, HostPort: *app.AllocatedHostPort.Value, Secrets: []SecretBinding{}, Units: *app.QuadletUnits.Value, CaddyFile: s.CaddyConfig.Value.Files[0]})
		}
	}
	raw, _ := json.Marshal(state)
	state = BrineState{}
	if e = json.Unmarshal(raw, &state); e != nil {
		t.Fatal(e)
	}
	return Input{Desired: d, Snapshot: s, Image: image, State: state}
}
func build(t testing.TB, in Input) Plan {
	t.Helper()
	p, e := Build(in)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func installed(t testing.TB) Input { return fixture(t, "one-app") }
func routes(in *Input, app string, domains ...string) {
	in.Snapshot.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{{Name: "hello.caddy", App: app, Domains: target.Known(domains)}})
}

func TestFixtureMatrix(t *testing.T) {
	tests := []struct {
		name, fixture string
		change        func(*Input)
		kind          Kind
		conflict      ConflictCode
	}{
		{"create", "ready-arm64", nil, Create, ""},
		{"skip occupied", "port-conflict", nil, Create, ""},
		{"unenrolled host", "fresh-arm64", nil, Conflict, RuntimeUnavailable},
		{"no-op", "one-app", nil, NoOp, ""},
		{"image update", "one-app", func(i *Input) {
			i.Desired.Image = spec.ImageReference(strings.ReplaceAll(string(i.Desired.Image), strings.Repeat("a", 64), strings.Repeat("e", 64)))
			i.Image.Digest = "sha256:" + strings.Repeat("e", 64)
		}, Update, ""},
		{"domain update", "one-app", func(i *Input) { i.Desired.Domains = []spec.Domain{"other.example.net"} }, Update, ""},
		{"env update", "one-app", func(i *Input) { i.Desired.Environment = []policy.Environment{{Name: "APP_ENV", Value: "production"}} }, Update, ""},
		{"domain conflict", "one-app", func(i *Input) { routes(i, "other", "hello.example.com") }, Conflict, DomainOwned},
		{"foreign route", "one-app", func(i *Input) { routes(i, "", "hello.example.com") }, Conflict, DomainOwned},
		{"port conflict", "one-app", func(i *Input) { i.Snapshot.PortOwners = target.Known([]target.PortOwner{{Port: 20000, App: "other"}}) }, Conflict, PortOwned},
		{"unattributed port", "one-app", func(i *Input) { i.Snapshot.PortOwners = target.Known([]target.PortOwner{}) }, Conflict, PortOwned},
		{"unsupported", "unsupported-ubuntu", nil, Conflict, UnsupportedTarget},
		{"platform", "one-app", func(i *Input) { i.Image.Platform.Arch = "amd64" }, Conflict, ImagePlatform},
		{"missing secret", "one-app", func(i *Input) { i.Desired.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "hello-token"}} }, Conflict, SecretMissing},
		{"unknown ports", "one-app", func(i *Input) { i.Snapshot.UsedPorts = target.Observation[[]target.Port]{Status: target.Unknown} }, Conflict, UnknownFacts},
		{"unknown generation", "one-app", func(i *Input) { i.Snapshot.Generation = target.Observation[uint64]{Status: target.Unknown} }, Conflict, UnknownFacts},
		{"unknown routes", "one-app", func(i *Input) {
			i.Snapshot.LiveCaddyFiles = target.Observation[[]target.LiveCaddyFile]{Status: target.Unknown}
		}, Conflict, UnknownFacts},
		{"unknown route domains", "one-app", func(i *Input) {
			(*i.Snapshot.LiveCaddyFiles.Value)[0].Domains = target.Observation[[]string]{Status: target.Unknown}
		}, Conflict, UnknownFacts},
		{"unsupported observation", "one-app", func(i *Input) { i.Snapshot.UsedPorts = target.Observation[[]target.Port]{Status: target.Unsupported} }, Conflict, UnsupportedTarget},
		{"port exhaustion", "port-conflict", func(i *Input) { i.Desired.AppPorts = policy.PortRange{Min: 20000, Max: 20000} }, Conflict, PortsExhausted},
		{"artifact drift", "one-app", func(i *Input) {
			(*(*i.Snapshot.Apps.Value)[0].QuadletUnits.Value)[0].Hash = "sha256:" + strings.Repeat("f", 64)
		}, Conflict, ArtifactDrift},
		{"missing passt", "missing-passt", nil, Conflict, RuntimeUnavailable},
		{"unknown runtime", "unknown-runtime", nil, Conflict, UnknownFacts},
		{"cgroup false", "cgroup-v1", nil, Conflict, RuntimeUnavailable},
		{"runner not ready", "one-app", func(i *Input) { i.Snapshot.Runner.Linger = target.Known(false) }, Conflict, RuntimeUnavailable},
		{"state identity mismatch", "one-app", func(i *Input) { i.State.Target.ID = "other-target" }, Conflict, StaleState},
		{"state generation mismatch", "one-app", func(i *Input) { i.State.Generation++ }, Conflict, StaleState},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := fixture(t, tt.fixture)
			if tt.change != nil {
				tt.change(&in)
			}
			p := build(t, in)
			if p.Kind != tt.kind {
				t.Fatalf("kind %s want %s: %+v", p.Kind, tt.kind, p.Conflicts)
			}
			if tt.conflict != "" {
				found := false
				for _, d := range p.Conflicts {
					if d.Code == tt.conflict {
						found = true
					}
				}
				if !found {
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

func TestObservedAddressConflicts(t *testing.T) {
	for _, address := range []string{"HELLO.EXAMPLE.COM", "https://HELLO.EXAMPLE.COM:443", "http://hello.example.com:80", "hello.example.com.", "*.EXAMPLE.COM", ":443"} {
		t.Run(address, func(t *testing.T) {
			in := fixture(t, "ready-arm64")
			routes(&in, "other", address)
			p := build(t, in)
			if p.Kind != Conflict || len(p.Changes) != 0 || p.Conflicts[0].Code != DomainOwned {
				t.Fatalf("%+v", p)
			}
		})
	}
}

func TestSecretOnlyAppCannotReplaceForeignFile(t *testing.T) {
	in := installed(t)
	app := &(*in.Snapshot.Apps.Value)[0]
	app.Image = target.Observation[target.Image]{Status: target.Absent}
	app.AllocatedHostPort = target.Observation[target.Port]{Status: target.Absent}
	app.QuadletUnits = target.Known([]target.Unit{})
	in.State.Releases = []CurrentRelease{}
	in.Snapshot.UsedPorts = target.Known([]target.Port{})
	in.Snapshot.PortOwners = target.Known([]target.PortOwner{})
	routes(&in, "other", "unrelated.example.net")
	in.Desired.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}}
	p := build(t, in)
	if p.Kind != Conflict || len(p.Changes) != 0 || p.Conflicts[0].Code != ArtifactDrift {
		t.Fatalf("foreign namesake file was replaceable: %+v", p)
	}
}

func TestRequiredCapabilities(t *testing.T) {
	for _, name := range []string{"systemd", "podman", "passt", "caddy", "runner"} {
		for _, status := range []target.Status{target.Absent, target.Unknown, target.Unsupported} {
			t.Run(name+"/"+string(status), func(t *testing.T) {
				in := installed(t)
				o := target.Observation[string]{Status: status}
				switch name {
				case "systemd":
					in.Snapshot.Versions.Systemd = o
				case "podman":
					in.Snapshot.Versions.Podman = o
				case "passt":
					in.Snapshot.Versions.Passt = o
				case "caddy":
					in.Snapshot.Versions.Caddy = o
				case "runner":
					in.Snapshot.Runner.User = o
				}
				p := build(t, in)
				if p.Kind != Conflict || len(p.Changes) != 0 {
					t.Fatal(p.Kind)
				}
			})
		}
	}
	for _, change := range []func(*Input){func(i *Input) { i.Snapshot.Versions.Podman = target.Known("4.9.0") }, func(i *Input) { i.Snapshot.Versions.Systemd = target.Known("2570") }, func(i *Input) { i.Snapshot.Versions.Caddy = target.Known("2.10.0") }, func(i *Input) { i.Snapshot.Runner.User = target.Known("root") },
		func(i *Input) { i.Snapshot.Versions.Passt = target.Known("not-a-passt-version") }, func(i *Input) { i.Snapshot.CgroupV2 = target.Observation[bool]{Status: target.Unknown} }} {
		in := installed(t)
		change(&in)
		p := build(t, in)
		if p.Kind != Conflict {
			t.Fatal(p.Kind)
		}
	}
	in := installed(t)
	in.Snapshot.Versions.Litestream = target.Observation[string]{Status: target.Absent}
	if p := build(t, in); p.Kind != NoOp {
		t.Fatal("optional Litestream blocked plan", p.Conflicts)
	}
	in.Snapshot.Versions.Podman = target.Known("5.4.2+ds1-2")
	in.Snapshot.Versions.Systemd = target.Known("257.9-1~deb13u1")
	if p := build(t, in); p.Kind != NoOp {
		t.Fatal("distribution revisions blocked plan", p.Conflicts)
	}
}

func TestSecretVersionsAndRotation(t *testing.T) {
	in := installed(t)
	in.Desired.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}}
	app := &(*in.Snapshot.Apps.Value)[0]
	*app.Secrets.Value = append(*app.Secrets.Value, target.Secret{Name: "brine-hello-token-v10", ID: "synthetic-10"}, target.Secret{Name: "brine-other-token-v99", ID: "synthetic-99"})
	initial := build(t, in)
	if initial.Kind != Update || len(initial.Secrets) != 1 || initial.Secrets[0].VersionName != "brine-hello-token-v10" || initial.Secrets[0].ID != "synthetic-10" {
		t.Fatal(initial)
	}
	in.State.Releases[0].Desired = in.Desired
	in.State.Releases[0].Secrets = initial.Secrets
	if p := build(t, in); p.Kind != NoOp {
		t.Fatal(p.Kind)
	}
	*app.Secrets.Value = append(*app.Secrets.Value, target.Secret{Name: "brine-hello-token-v11", ID: "synthetic-11"})
	p := build(t, in)
	if p.Kind != Update || p.Secrets[0].VersionName != "brine-hello-token-v11" || p.Hash == initial.Hash {
		t.Fatal(p)
	}
}

func TestDeterminismAndPurity(t *testing.T) {
	in := installed(t)
	in.Desired.Environment = []policy.Environment{{Name: "B", Value: "b"}, {Name: "A", Value: "a"}}
	in.Desired.Domains = append(in.Desired.Domains, "other.example.net")
	in.State.Releases[0].Desired.Domains = slices.Clone(in.Desired.Domains)
	in.Desired.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}, {Name: "KEY", Reference: "key"}}
	in.Snapshot.UsedPorts = target.Known([]target.Port{20002, 20000})
	in.Snapshot.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{{Name: "hello.caddy", App: "hello", Domains: target.Known([]string{"other.example.net", "hello.example.com"})}, {Name: "other.caddy", App: "other", Domains: target.Known([]string{"other.example.org"})}})
	in.Snapshot.CaddyConfig.Value.Files = append(in.Snapshot.CaddyConfig.Value.Files, target.CaddyFile{Name: "other.caddy", Hash: "sha256:" + strings.Repeat("e", 64)})
	in.Snapshot.PortOwners = target.Known([]target.PortOwner{{Port: 20000, App: "hello"}, {Port: 20001, App: "other"}})
	other := (*in.Snapshot.Apps.Value)[0]
	other.Name = "other"
	other.AllocatedHostPort = target.Known(target.Port(20001))
	other.Secrets = target.Known(slices.Clone(*other.Secrets.Value))
	other.QuadletUnits = target.Known(slices.Clone(*other.QuadletUnits.Value))
	*in.Snapshot.Apps.Value = append(*in.Snapshot.Apps.Value, other)
	otherRelease := in.State.Releases[0]
	otherRelease.App = "other"
	otherRelease.Desired.Name = "other"
	otherRelease.Desired.Domains = []spec.Domain{"other.example.org"}
	otherRelease.HostPort = 20001
	otherRelease.Units = slices.Clone(otherRelease.Units)
	otherRelease.CaddyFile = in.Snapshot.CaddyConfig.Value.Files[1]
	in.State.Releases = append(in.State.Releases, otherRelease)
	before, _ := json.Marshal(in)
	p := build(t, in)
	if p.Kind != Update {
		t.Fatal("determinism fixture must exercise changes", p.Kind, p.Conflicts)
	}
	after, _ := json.Marshal(in)
	if !bytes.Equal(before, after) {
		t.Fatal("mutated input")
	}
	slices.Reverse(in.Desired.Domains)
	slices.Reverse(in.Desired.Environment)
	slices.Reverse(in.Desired.Secrets)
	slices.Reverse(*in.Snapshot.Apps.Value)
	slices.Reverse(*in.Snapshot.UsedPorts.Value)
	slices.Reverse(in.Snapshot.CaddyConfig.Value.Files)
	slices.Reverse(*in.Snapshot.PortOwners.Value)
	for _, a := range *in.Snapshot.Apps.Value {
		slices.Reverse(*a.QuadletUnits.Value)
		slices.Reverse(*a.Secrets.Value)
	}
	for _, f := range *in.Snapshot.LiveCaddyFiles.Value {
		slices.Reverse(*f.Domains.Value)
	}
	slices.Reverse(*in.Snapshot.LiveCaddyFiles.Value)
	slices.Reverse(in.State.Releases)
	for _, r := range in.State.Releases {
		slices.Reverse(r.Units)
		slices.Reverse(r.Secrets)
	}
	q := build(t, in)
	if !reflect.DeepEqual(p, q) {
		t.Fatalf("plans differ\n%+v\n%+v", p, q)
	}
	*in.Snapshot.Generation.Value = 999
	if *p.ObservedGeneration.Value == 999 {
		t.Fatal("aliased generation")
	}
}

func TestHashPreconditions(t *testing.T) {
	in := installed(t)
	p := build(t, in)
	for _, change := range []func(*Input){func(i *Input) { i.Snapshot.Generation = target.Known(uint64(5)) }, func(i *Input) { i.Snapshot.Identity.ID = "other-target" }, func(i *Input) { i.Desired.PolicyVersion = "operator-2" }, func(i *Input) { i.Desired.PolicyHash = "sha256:" + strings.Repeat("f", 64) }, func(i *Input) { i.Snapshot.CaddyConfig.Value.Files[0].Hash = "sha256:" + strings.Repeat("f", 64) }, func(i *Input) {
		i.State.Releases[0].Desired.Environment = []policy.Environment{{Name: "MODE", Value: "old"}}
	}, func(i *Input) { i.State.Releases[0].ID = "release-0002" }} {
		next := installed(t)
		change(&next)
		if build(t, next).Hash == p.Hash {
			t.Fatal("precondition not hashed")
		}
	}
}

func goldenInputs(t testing.TB) map[string]Input {
	conflict := installed(t)
	routes(&conflict, "other", "hello.example.com")
	update := installed(t)
	update.State.Releases[0].Desired.Environment = []policy.Environment{{Name: "REMOVED", Value: "SYNTHETIC_OLD_PRIVATE"}, {Name: "CHANGED", Value: "SYNTHETIC_OLD_PRIVATE"}}
	update.Desired.Environment = []policy.Environment{{Name: "ADDED", Value: "SYNTHETIC_NEW_PRIVATE"}, {Name: "CHANGED", Value: "SYNTHETIC_NEW_PRIVATE"}}
	update.Desired.Domains = []spec.Domain{"new.example.com"}
	return map[string]Input{"create": fixture(t, "ready-arm64"), "update": update, "no-op": installed(t), "conflict": conflict}
}
func TestGolden(t *testing.T) {
	for name, in := range goldenInputs(t) {
		t.Run(name, func(t *testing.T) {
			b, e := build(t, in).CanonicalBytes()
			if e != nil {
				t.Fatal(e)
			}
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if e := os.WriteFile("testdata/"+name+".json", b, 0644); e != nil {
					t.Fatal(e)
				}
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

func TestChangeOrderAndPreservedCaddy(t *testing.T) {
	in := installed(t)
	in.Snapshot.CaddyConfig.Value.Files = append(in.Snapshot.CaddyConfig.Value.Files, target.CaddyFile{Name: "unrelated.caddy", Hash: "sha256:" + strings.Repeat("f", 64)})
	in.Desired.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}}
	p := build(t, in)
	if p.Kind != Update || p.HostPort != 20000 {
		t.Fatal(p.Kind, p.HostPort)
	}
	kinds := []ChangeKind{}
	for _, c := range p.Changes {
		kinds = append(kinds, c.Kind)
	}
	if !reflect.DeepEqual(kinds, []ChangeKind{PullImage, BindSecret, RenderQuadlet, StageCaddy, RestartApp}) {
		t.Fatal(kinds)
	}
	caddy := p.Changes[3].Caddy
	if caddy.Previous != 2 || caddy.Next != 3 || len(caddy.Preserve) != 1 || caddy.Preserve[0].Name != "unrelated.caddy" {
		t.Fatal(caddy)
	}
}

func TestReservedPortsAndPrecreatedSecrets(t *testing.T) {
	in := installed(t)
	app := &(*in.Snapshot.Apps.Value)[0]
	app.Image = target.Observation[target.Image]{Status: target.Absent}
	app.QuadletUnits = target.Known([]target.Unit{})
	app.AllocatedHostPort = target.Observation[target.Port]{Status: target.Absent}
	in.State.Releases = []CurrentRelease{}
	in.Snapshot.UsedPorts = target.Known([]target.Port{})
	in.Snapshot.PortOwners = target.Known([]target.PortOwner{})
	in.Snapshot.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{})
	in.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Files: []target.CaddyFile{}})
	in.Desired.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}}
	p := build(t, in)
	if p.Kind != Create || p.HostPort != 20000 || len(p.Secrets) != 1 {
		t.Fatal(p)
	}
	other := *app
	other.Name = "other"
	other.AllocatedHostPort = target.Known(target.Port(20000))
	*in.Snapshot.Apps.Value = append(*in.Snapshot.Apps.Value, other)
	if p = build(t, in); p.HostPort != 20001 {
		t.Fatal(p.HostPort)
	}
}

func TestMalformedStateAndPinnedMetadata(t *testing.T) {
	for _, change := range []func(*Input){func(i *Input) { i.Image.Digest = "sha256:" + strings.Repeat("f", 64) }, func(i *Input) { i.State.Releases = nil }, func(i *Input) { i.State.Releases = append(i.State.Releases, i.State.Releases[0]) }, func(i *Input) { i.State.Releases[0].CaddyFile.Name = "unrelated.caddy" }, func(i *Input) { i.Snapshot.PortOwners = target.Known([]target.PortOwner{{Port: 0}}) }} {
		in := installed(t)
		change(&in)
		p, e := Build(in)
		if e == nil || !reflect.DeepEqual(p, Plan{}) {
			t.Fatalf("expected zero plan and error, got %+v, %v", p, e)
		}
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
	in.State.Releases[0].Units = []target.Unit{}
	if p := build(t, in); p.Kind == NoOp {
		t.Fatal("missing container unit is not a no-op")
	}
	in = installed(t)
	in.Snapshot.CaddyConfig = target.Observation[target.CaddyConfigSet]{Status: target.Absent}
	if p := build(t, in); p.Kind != Conflict {
		t.Fatal("missing committed Caddy file must conflict")
	}
}

func TestCommittedStateMustMatchLiveArtifacts(t *testing.T) {
	for _, change := range []func(*Input){
		func(i *Input) { i.Snapshot.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{}) },
		func(i *Input) { routes(i, "other", "unrelated.example.net") },
		func(i *Input) { i.State.Releases[0].HostPort = 20001 },
	} {
		in := installed(t)
		change(&in)
		p := build(t, in)
		if p.Kind != Conflict || len(p.Changes) != 0 {
			t.Fatal(p.Kind)
		}
	}
}

func FuzzHashEquality(f *testing.F) {
	f.Add(uint16(7), "blue", true)
	f.Add(uint16(19), "green", false)
	f.Fuzz(func(t *testing.T, n uint16, value string, shuffle bool) {
		if len(value) > 100 {
			t.Skip()
		}
		in := fixture(t, "ready-arm64")
		in.Snapshot.Generation = target.Known(uint64(n))
		in.State.Generation = uint64(n)
		in.Desired.Environment = []policy.Environment{{Name: "A", Value: value}, {Name: "B", Value: "fixed"}}
		p := build(t, in)
		if shuffle {
			slices.Reverse(in.Desired.Environment)
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

func twoAppState(t testing.TB) Input {
	t.Helper()
	in := installed(t)
	other := in.State.Releases[0]
	other.App = "other"
	other.ID = "release-other-0001"
	other.Desired.Name = "other"
	other.Desired.Domains = []spec.Domain{"other.example.net"}
	other.Units = []target.Unit{{Name: "other.container", Hash: "sha256:" + strings.Repeat("e", 64)}}
	other.CaddyFile = target.CaddyFile{Name: "other.caddy", Hash: "sha256:" + strings.Repeat("f", 64)}
	in.State.Releases = append(in.State.Releases, other)
	in.Snapshot.CaddyConfig.Value.Files = append(in.Snapshot.CaddyConfig.Value.Files, other.CaddyFile)
	*in.Snapshot.LiveCaddyFiles.Value = append(*in.Snapshot.LiveCaddyFiles.Value, target.LiveCaddyFile{Name: "other.caddy", App: "other", Domains: target.Known([]string{"other.example.net"})})
	in.Desired.Environment = []policy.Environment{{Name: "APP_ENV", Value: "changed"}}
	return in
}

func TestGenerationCannotDropOrCopyDriftedCommittedApp(t *testing.T) {
	for _, scenario := range []string{"missing file", "changed hash", "missing live file", "changed live association", "changed live domain"} {
		t.Run(scenario, func(t *testing.T) {
			in := twoAppState(t)
			switch scenario {
			case "missing file":
				in.Snapshot.CaddyConfig.Value.Files = in.Snapshot.CaddyConfig.Value.Files[:1]
			case "changed hash":
				in.Snapshot.CaddyConfig.Value.Files[1].Hash = "sha256:" + strings.Repeat("c", 64)
			case "missing live file":
				*in.Snapshot.LiveCaddyFiles.Value = (*in.Snapshot.LiveCaddyFiles.Value)[:1]
			case "changed live association":
				(*in.Snapshot.LiveCaddyFiles.Value)[1].App = ""
			case "changed live domain":
				(*in.Snapshot.LiveCaddyFiles.Value)[1].Domains = target.Known([]string{"unrelated.example.net"})
			}
			p := build(t, in)
			if p.Kind != Conflict || len(p.Changes) != 0 {
				t.Fatalf("unsafe whole-generation plan: kind=%s conflicts=%+v changes=%d", p.Kind, p.Conflicts, len(p.Changes))
			}
			found := false
			for _, d := range p.Conflicts {
				if d.Code == ArtifactDrift {
					found = true
				}
			}
			if !found {
				t.Fatal("expected artifact drift", p.Conflicts)
			}
		})
	}
}

func TestGenerationPreservesMatchingCommittedAndUnmanagedFiles(t *testing.T) {
	in := twoAppState(t)
	unmanaged := target.CaddyFile{Name: "unmanaged.caddy", Hash: "sha256:" + strings.Repeat("b", 64)}
	in.Snapshot.CaddyConfig.Value.Files = append(in.Snapshot.CaddyConfig.Value.Files, unmanaged)
	*in.Snapshot.LiveCaddyFiles.Value = append(*in.Snapshot.LiveCaddyFiles.Value, target.LiveCaddyFile{Name: unmanaged.Name, Domains: target.Known([]string{"unmanaged.example.net"})})
	p := build(t, in)
	if p.Kind != Update {
		t.Fatal(p.Kind, p.Conflicts)
	}
	for _, change := range p.Changes {
		if change.Kind == StageCaddy {
			want := []target.CaddyFile{in.State.Releases[1].CaddyFile, unmanaged}
			if !reflect.DeepEqual(change.Caddy.Preserve, want) {
				t.Fatalf("preserve %+v want %+v", change.Caddy.Preserve, want)
			}
			return
		}
	}
	t.Fatal("missing Caddy generation staging change")
}
