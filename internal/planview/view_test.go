package planview

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/ui"
	"github.com/charmbracelet/x/ansi"
)

func example(kind plan.Kind) plan.Plan {
	image := plan.Image{ManifestDigest: target.Observation[string]{Status: target.Unknown}, Digest: "sha256:" + strings.Repeat("a", 64), Platform: target.Platform{OS: "linux", Arch: "arm64"}}
	p := plan.Plan{SchemaVersion: 1, Kind: kind, App: "hello", Image: image, HostPort: 20000, Secrets: []plan.SecretBinding{}, Changes: []plan.Change{}, Conflicts: []plan.Diagnostic{}}
	if kind == plan.Create || kind == plan.Update {
		p.Changes = []plan.Change{
			{Kind: plan.AllocatePort, Allocation: &plan.PortAllocation{App: "hello", Port: 20000}},
			{Kind: plan.PullImage, Image: &image},
			{Kind: plan.BindSecret, Secret: &plan.SecretBinding{Environment: "TOKEN", Reference: "hello-token", ID: "SYNTHETIC_PRIVATE_ID"}},
			{Kind: plan.RenderQuadlet, Quadlet: &plan.Quadlet{Desired: policy.Desired{ContainerPort: 3000, Domains: []spec.Domain{"hello.example.com"}, Environment: []policy.Environment{{Name: "APP_ENV", Value: "SYNTHETIC_PRIVATE_VALUE"}}}}},
			{Kind: plan.StageCaddy, Caddy: &plan.CaddyGeneration{Previous: 2, Next: 3, Domains: []spec.Domain{"hello.example.com"}, HostPort: 20000}},
			{Kind: plan.RestartApp, Restart: &plan.Restart{App: "hello"}},
		}
	}

	if kind == plan.Create || kind == plan.Update {
		port := target.Port(20000)
		containerPort := spec.Port(3000)
		resources := policy.Resources{MemoryMB: 512, PIDsLimit: 128}
		health := policy.Health{Path: "/ready", ExpectedStatus: 200, StartupDeadlineSeconds: 30, TimeoutSeconds: 3}
		p.Diff = &plan.ConfigurationDiff{
			Image:         &plan.ValueChange[plan.Image]{To: &image},
			Domains:       &plan.SetChange[spec.Domain]{Added: []spec.Domain{"hello.example.com"}, Removed: []spec.Domain{}},
			HostPort:      &plan.ValueChange[target.Port]{To: &port},
			ContainerPort: &plan.ValueChange[spec.Port]{To: &containerPort},
			Resources:     &plan.ValueChange[policy.Resources]{To: &resources},
			Health:        &plan.ValueChange[policy.Health]{To: &health},
			Environment:   &plan.EnvironmentChange{Added: []string{"APP_ENV"}, Removed: []string{}, Changed: []string{}},
			Secrets:       []plan.SecretChange{{Environment: "TOKEN", To: &plan.SecretVersion{Reference: "hello-token", VersionName: "brine.hello.hello-token.v2"}}},
		}
		if kind == plan.Update {
			oldImage := plan.Image{ManifestDigest: target.Observation[string]{Status: target.Unknown}, Digest: "sha256:" + strings.Repeat("b", 64), Platform: image.Platform}
			oldContainer := spec.Port(4000)
			oldHost := target.Port(21000)
			oldResources := policy.Resources{MemoryMB: 256, PIDsLimit: 64}
			oldHealth := policy.Health{Path: "/", ExpectedStatus: 200, StartupDeadlineSeconds: 15, TimeoutSeconds: 2}
			p.Diff.Image.From = &oldImage
			p.Diff.HostPort.From = &oldHost
			p.Diff.ContainerPort.From = &oldContainer
			p.Diff.Resources.From = &oldResources
			p.Diff.Health.From = &oldHealth
			p.Diff.Domains.Removed = []spec.Domain{"old.example.com"}
			p.Diff.Environment = &plan.EnvironmentChange{Added: []string{"ADDED"}, Removed: []string{"REMOVED"}, Changed: []string{"APP_ENV"}}
			p.Diff.Secrets[0].From = &plan.SecretVersion{Reference: "hello-token", VersionName: "brine.hello.hello-token.v1"}
		}
	}
	if kind == plan.Conflict {
		p.Conflicts = []plan.Diagnostic{{Code: plan.DomainOwned, Field: "domains"}, {Code: plan.SecretMissing, Field: "secrets.TOKEN"}}
	}
	return p
}

func TestGolden(t *testing.T) {
	for _, kind := range []plan.Kind{plan.Create, plan.Update, plan.NoOp, plan.Conflict} {
		t.Run(string(kind), func(t *testing.T) {
			got := Human(example(kind), ui.NewTheme(true), 80)
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile("testdata/"+string(kind)+".golden", []byte(got), 0644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile("testdata/" + string(kind) + ".golden")
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestSafeDeterministicJSON(t *testing.T) {
	p := example(plan.Update)
	p.Secrets = []plan.SecretBinding{{Environment: "TOKEN", Reference: "hello-token", ID: "SYNTHETIC_PRIVATE_ID", VersionName: "SYNTHETIC_PRIVATE_VERSION"}}
	before, _ := json.Marshal(p)
	a, err := JSON(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := JSON(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) || !json.Valid(a) {
		t.Fatal("unstable or invalid JSON")
	}
	for _, out := range []string{string(a), Human(p, ui.NewTheme(true), 80)} {
		if strings.Contains(out, "SYNTHETIC_PRIVATE") {
			t.Fatal("private value exposed")
		}
	}
	after, _ := json.Marshal(p)
	if !bytes.Equal(before, after) {
		t.Fatal("mutated plan")
	}
	for _, width := range []int{40, 80} {
		for _, line := range strings.Split(Human(p, ui.NewTheme(true), width), "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("width %d exceeded: %q", width, line)
			}
			if strings.Contains(line, "\x1b") {
				t.Fatal("plain output contains escape")
			}
		}
	}
}

func FuzzRender(f *testing.F) {
	f.Add([]byte(`{"kind":"update","changes":[{"kind":"pull_image"}]}`))
	seed, _ := json.Marshal(example(plan.Create))
	f.Add(bytes.ReplaceAll(seed, []byte("SYNTHETIC_PRIVATE"), []byte("fixture")))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<16 || bytes.Contains(b, []byte("SYNTHETIC_PRIVATE")) {
			t.Skip()
		}
		var p plan.Plan
		if json.Unmarshal(b, &p) != nil {
			return
		}
		for i := range p.Secrets {
			p.Secrets[i].ID = "SYNTHETIC_PRIVATE_ID"
			p.Secrets[i].VersionName = "SYNTHETIC_PRIVATE_VERSION"
		}
		for i := range p.Changes {
			if p.Changes[i].Quadlet != nil {
				p.Changes[i].Quadlet.Desired.Environment = []policy.Environment{{Name: "TOKEN", Value: "SYNTHETIC_PRIVATE_VALUE"}}
			}
			if p.Changes[i].Secret != nil {
				p.Changes[i].Secret.ID = "SYNTHETIC_PRIVATE_ID"
			}
		}
		a, err := JSON(p)
		if err != nil {
			t.Fatal(err)
		}
		c, err := JSON(p)
		if err != nil || !bytes.Equal(a, c) {
			t.Fatal("nondeterministic")
		}
		for _, out := range []string{string(a), Human(p, ui.NewTheme(true), 40)} {
			if strings.Contains(out, "SYNTHETIC_PRIVATE") {
				t.Fatal("leaked value")
			}
		}
	})
}

func TestJSONFieldOrder(t *testing.T) {
	b, err := JSON(example(plan.Create))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	if _, err = decoder.Token(); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, token.(string))
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"schema_version", "kind", "app", "target", "observed_generation", "policy_version", "policy_hash", "desired_hash", "config_hash", "image", "host_port", "secrets", "changes", "diff", "conflicts", "hash"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatal(keys)
	}
	if bytes.Contains(b, []byte("\x1b")) || b[len(b)-1] == '\n' {
		t.Fatal("unexpected machine formatting")
	}
}

func TestDigestCollisionAndControlSafety(t *testing.T) {
	p := example(plan.Create)
	p.Image.Digest = "sha256:" + strings.Repeat("a", 12) + strings.Repeat("b", 52)
	p.Changes[1].Image = &plan.Image{ManifestDigest: target.Observation[string]{Status: target.Unknown}, Digest: "sha256:" + strings.Repeat("a", 12) + strings.Repeat("c", 52)}
	got := Human(p, ui.NewTheme(true), 80)
	if !strings.Contains(got, "sha256:"+strings.Repeat("a", 12)+"c...") {
		t.Fatal("ambiguous digest abbreviation", got)
	}
	p.App = "hello\x1b[31m\nforged headline"
	got = Human(p, ui.NewTheme(true), 40)
	if strings.Contains(got, "\x1b") {
		t.Fatal("terminal control escaped sanitization")
	}
	for _, line := range strings.Split(got, "\n") {
		if ansi.StringWidth(line) > 40 {
			t.Fatal("overlong line", line)
		}
	}
}

func TestPresentationCanonicalSets(t *testing.T) {
	p := example(plan.Update)
	p.Secrets = []plan.SecretBinding{{Environment: "Z", Reference: "last"}, {Environment: "A", Reference: "first"}}
	p.Changes[3].Quadlet.EnvironmentKeys = []string{"Z", "A"}
	p.Changes[4].Caddy.Domains = []spec.Domain{"z.example.com", "a.example.com"}
	a, _ := JSON(p)
	human := Human(p, ui.NewTheme(true), 80)
	p.Secrets[0], p.Secrets[1] = p.Secrets[1], p.Secrets[0]
	env := p.Changes[3].Quadlet.EnvironmentKeys
	env[0], env[1] = env[1], env[0]
	domains := p.Changes[4].Caddy.Domains
	domains[0], domains[1] = domains[1], domains[0]
	b, _ := JSON(p)
	if !bytes.Equal(a, b) || human != Human(p, ui.NewTheme(true), 80) {
		t.Fatal("set permutation changed presentation")
	}
}

func TestBuiltPlanPresentation(t *testing.T) {
	read := func(path string) []byte {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	operator, err := policy.Parse(bytes.ReplaceAll(read("../policy/testdata/operator.toml"), []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(bytes.ReplaceAll(read("../spec/testdata/valid-minimal.toml"), []byte("example/hello"), []byte("team/hello")))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []plan.Kind{plan.Create, plan.Update, plan.NoOp, plan.Conflict} {
		t.Run(string(kind), func(t *testing.T) {
			desired, err := policy.Normalize(app, operator)
			if err != nil {
				t.Fatal(err)
			}
			name := "ready-arm64"
			if kind == plan.Update || kind == plan.NoOp {
				name = "one-app"
			}
			snapshot, err := target.Decode(read("../target/testdata/" + name + ".json"))
			if err != nil {
				t.Fatal(err)
			}
			image := plan.Image{ManifestDigest: target.Observation[string]{Status: target.Unknown}, Digest: strings.Split(string(desired.Image), "@")[1], Platform: target.Platform{OS: "linux", Arch: snapshot.Arch}}
			state := plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{}}
			if name == "one-app" {
				observed := (*snapshot.Apps.Value)[0]
				state.Releases = append(state.Releases, plan.CurrentRelease{App: "hello", ID: "release-0001", Desired: desired, Image: plan.Image{Digest: observed.Image.Value.Digest, Platform: observed.Image.Value.Platform, ManifestDigest: target.Observation[string]{Status: target.Unknown}}, HostPort: *observed.AllocatedHostPort.Value, Secrets: []plan.SecretBinding{}, Units: *observed.QuadletUnits.Value, CaddyFile: snapshot.CaddyConfig.Value.Files[0]})
			}
			if kind == plan.Create || kind == plan.Update {
				desired.Environment = []policy.Environment{{Name: "TOKEN", Value: "SYNTHETIC_PRIVATE_VALUE"}}
			}
			if kind == plan.Conflict {
				snapshot.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{{Name: "foreign.caddy", App: "other", Domains: target.Known([]string{"hello.example.com"})}})
			}
			p, err := plan.Build(plan.Input{Desired: desired, Snapshot: snapshot, Image: image, State: state})
			if err != nil {
				t.Fatal(err)
			}
			if p.Kind != kind {
				t.Fatalf("got %s want %s", p.Kind, kind)
			}
			b, err := JSON(p)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Kind plan.Kind `json:"kind"`
				Hash string    `json:"hash"`
			}
			if err = json.Unmarshal(b, &result); err != nil {
				t.Fatal(err)
			}
			if result.Kind != p.Kind || result.Hash != p.Hash {
				t.Fatal("presentation lost plan identity")
			}
			for _, out := range []string{string(b), Human(p, ui.NewTheme(true), 40), Human(p, ui.NewTheme(true), 80)} {
				if strings.Contains(out, "SYNTHETIC_PRIVATE") {
					t.Fatal("built plan leaked value")
				}
			}
		})
	}
}

func TestPlainWidthsAndNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, kind := range []plan.Kind{plan.Create, plan.Update, plan.NoOp, plan.Conflict} {
		for _, width := range []int{1, 40, 80, 120} {
			out := Human(example(kind), ui.ThemeFromEnv(), width)
			if strings.Contains(out, "\x1b") {
				t.Fatal("NO_COLOR output contains ANSI")
			}
			for _, line := range strings.Split(out, "\n") {
				if ansi.StringWidth(line) > min(width, 80) {
					t.Fatalf("%s overflows width %d", kind, width)
				}
			}
		}
	}
}

func TestTypedDiffCanonicalAndImmutable(t *testing.T) {
	p := example(plan.Update)
	p.Diff.Domains.Added = []spec.Domain{"z.example.com", "a.example.com"}
	p.Diff.Environment.Added = []string{"Z", "A"}
	p.Diff.Environment.Removed = []string{"REMOVED_Z", "REMOVED_A"}
	p.Diff.Secrets = append(p.Diff.Secrets, plan.SecretChange{Environment: "REMOVED", From: &plan.SecretVersion{Reference: "removed-token", VersionName: "brine.hello.removed-token.v1"}})
	before, _ := json.Marshal(p)
	a, err := JSON(p)
	if err != nil {
		t.Fatal(err)
	}
	human := Human(p, ui.NewTheme(true), 80)
	after, _ := json.Marshal(p)
	if !bytes.Equal(before, after) {
		t.Fatal("projection mutated diff")
	}
	slices.Reverse(p.Diff.Domains.Added)
	slices.Reverse(p.Diff.Environment.Added)
	slices.Reverse(p.Diff.Environment.Removed)
	slices.Reverse(p.Diff.Secrets)
	b, err := JSON(p)
	if err != nil || !bytes.Equal(a, b) || human != Human(p, ui.NewTheme(true), 80) {
		t.Fatal("diff permutation changed output")
	}
	if !strings.Contains(human, "- secret REMOVED: removed-token") {
		t.Fatal("missing secret removal")
	}
}

func TestManifestPresentation(t *testing.T) {
	p := example(plan.NoOp)
	manifest := "sha256:" + strings.Repeat("a", 12) + strings.Repeat("b", 52)
	p.Image.ManifestDigest = target.Known(manifest)
	raw, err := JSON(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"manifest_digest":{"status":"known","value":"`+manifest+`"}`)) {
		t.Fatal("full known manifest missing from JSON")
	}
	human := Human(p, ui.NewTheme(true), 80)
	if !strings.Contains(human, "Platform manifest: sha256:"+strings.Repeat("a", 12)+"b...") || strings.Contains(human, manifest) {
		t.Fatalf("manifest must be shortened without colliding with index: %s", human)
	}
	p.Image.ManifestDigest = target.Observation[string]{Status: target.Unknown}
	raw, err = JSON(p)
	if err != nil || !bytes.Contains(raw, []byte(`"manifest_digest":{"status":"unknown"}`)) {
		t.Fatal("unknown manifest missing from JSON")
	}
	if !strings.Contains(Human(p, ui.NewTheme(true), 80), "Platform manifest: unknown") {
		t.Fatal("unknown manifest missing from human output")
	}
}
