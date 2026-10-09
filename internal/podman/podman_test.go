package podman

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

type recorder struct {
	commands []localexec.Command
	inputs   []string
	results  []localexec.Result
	err      error
	errors   []error
}

func (r *recorder) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	r.commands = append(r.commands, c)
	err := r.err
	if len(r.errors) > 0 {
		err = r.errors[0]
		r.errors = r.errors[1:]
	}
	input := ""
	if c.Stdin != nil {
		input = string(c.Stdin)
	}
	r.inputs = append(r.inputs, input)
	if len(r.results) == 0 {
		return localexec.Result{}, err
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result, err
}
func client(t *testing.T, r *recorder) *Client {
	t.Helper()
	s, err := localexec.NewSession(r, 1234, "/tmp", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return New(s)
}
func image(t *testing.T) Image {
	t.Helper()
	v, e := ParseImage("registry.example/app@sha256:" + strings.Repeat("a", 64))
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func name(t *testing.T) Name {
	t.Helper()
	v, e := ParseName("brine-app")
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func fixture(t *testing.T, file string) string {
	t.Helper()
	b, e := os.ReadFile("testdata/" + file)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func TestArgv(t *testing.T) {
	ctx := context.Background()
	im := image(t)
	n := name(t)
	tests := []struct {
		want     []string
		mutation bool
		input    string
		run      func(*Client) error
		result   string
	}{
		{[]string{"version", "--format", "json"}, false, "", func(c *Client) error { _, e := c.Version(ctx); return e }, `{"Client":{"Version":"5.4.2","OsArch":"linux/arm64"}}`},
		{[]string{"pull", im.String()}, true, "", func(c *Client) error { return c.Pull(ctx, im) }, ""},
		{[]string{"image", "exists", im.String()}, false, "", func(c *Client) error { _, e := c.ImageExists(ctx, im); return e }, ""},
		{[]string{"secret", "create", n.String(), "-"}, true, "synthetic secret; $(no-shell)", func(c *Client) error {
			return c.CreateSecret(ctx, n, []byte("synthetic secret; $(no-shell)"))
		}, ""},
		{[]string{"secret", "exists", n.String()}, false, "", func(c *Client) error { _, e := c.SecretExists(ctx, n); return e }, ""},
		{[]string{"secret", "ls", "--format", "{{.Name}}"}, false, "", func(c *Client) error { _, e := c.SecretNames(ctx); return e }, "brine-app\n"},
		{[]string{"container", "inspect", n.String()}, false, "", func(c *Client) error { _, e := c.ContainerState(ctx, n); return e }, `[{"State":{"Status":"running","Running":true,"ExitCode":0}}]`},
	}
	for _, tt := range tests {
		r := &recorder{results: []localexec.Result{{Stdout: tt.result}}}
		if tt.want[0] == "container" {
			r.results = append([]localexec.Result{{}}, r.results...)
		}
		c := client(t, r)
		if e := tt.run(c); e != nil {
			t.Fatal(e)
		}
		cmd := r.commands[len(r.commands)-1]
		if cmd.Path != "podman" || !reflect.DeepEqual(cmd.Args, tt.want) || cmd.Mutation != tt.mutation || r.inputs[len(r.inputs)-1] != tt.input {
			t.Fatalf("command mismatch: %#v", cmd)
		}
		if cmd.Dir != "/tmp" || cmd.Timeout != time.Second || !reflect.DeepEqual(cmd.Env, []string{"XDG_RUNTIME_DIR=/run/user/1234", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1234/bus", "LC_ALL=C"}) {
			t.Fatalf("missing session: %#v", cmd)
		}
	}
}

// Captured with podman version --format json on Debian 13 arm64, Podman 5.4.2.
func TestRealVersionFixture(t *testing.T) {
	r := &recorder{results: []localexec.Result{{Stdout: fixture(t, "version.json")}}}
	v, e := client(t, r).Version(context.Background())
	if e != nil || v.Version != "5.4.2" || v.Platform.Architecture != "arm64" {
		t.Fatalf("version = %#v %v", v, e)
	}
}

// Captured from one public multi-arch image in throwaway rootless vfs storage
// on Podman 5.4.2. Repository names, lookup digests and storage paths are scrubbed.
func TestInspectBindsIndexAndPlatform(t *testing.T) {
	r := &recorder{results: []localexec.Result{{}, {Stdout: fixture(t, "image-inspect.json")}, {Stdout: fixture(t, "manifest-inspect.json")}, {Stdout: fixture(t, "platform-image-inspect.json")}}}
	got, e := client(t, r).Inspect(context.Background(), image(t))
	if e != nil || got.IndexDigest != "sha256:"+strings.Repeat("a", 64) || got.ManifestDigest != "sha256:"+strings.Repeat("b", 64) || got.Platform.Architecture != "arm64" {
		t.Fatalf("inspect = %#v %v", got, e)
	}
	want := [][]string{{"image", "exists", image(t).String()}, {"image", "inspect", image(t).String()}, {"manifest", "inspect", image(t).String()}, {"image", "inspect", "registry.example/app@sha256:" + strings.Repeat("b", 64)}}
	for i, c := range r.commands {
		if !reflect.DeepEqual(c.Args, want[i]) {
			t.Fatalf("argv = %#v", c.Args)
		}
	}
}
func TestExistsClassification(t *testing.T) {
	for _, code := range []int{1, 125} {
		r := &recorder{err: &localexec.Error{Kind: localexec.Failed, ExitCode: code}}
		found, e := client(t, r).ImageExists(context.Background(), image(t))
		if found || (code == 1 && e != nil) || (code == 125 && e == nil) {
			t.Fatalf("exists = %v %v", found, e)
		}
	}
	r := &recorder{err: &localexec.Error{Kind: localexec.Timeout}}
	_, e := client(t, r).SecretExists(context.Background(), name(t))
	var re *localexec.Error
	if !errors.As(e, &re) || re.Kind != localexec.Timeout {
		t.Fatal(e)
	}
}
func TestRejectInputsAndZeroValues(t *testing.T) {
	for _, v := range []string{"--help", "a; echo secret", "a\nname", "a=flag", ""} {
		if _, e := ParseName(v); e == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	for _, v := range []string{"registry.example/app=flag@sha256:" + strings.Repeat("a", 64), "registry.example/app:latest", "--help@sha256:" + strings.Repeat("a", 64), "registry.example/app@sha256:abc"} {
		if _, e := ParseImage(v); e == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	r := &recorder{}
	c := client(t, r)
	if e := c.Pull(context.Background(), Image{}); e == nil {
		t.Fatal("zero image accepted")
	}
	if e := c.CreateSecret(context.Background(), Name{}, []byte("s")); e == nil {
		t.Fatal("zero name accepted")
	}
	if len(r.commands) != 0 {
		t.Fatal("invalid input executed")
	}
}
func TestMalformedOutputDoesNotLeak(t *testing.T) {
	r := &recorder{results: []localexec.Result{{Stdout: "private-secret"}}}
	_, e := client(t, r).Version(context.Background())
	if e == nil || strings.Contains(e.Error(), "private-secret") {
		t.Fatalf("error = %v", e)
	}
}
func TestFake(t *testing.T) {
	var api Adapter = &Fake{VersionFunc: func(context.Context) (Version, error) { return Version{Version: "fake"}, nil }}
	v, e := api.Version(context.Background())
	if e != nil || v.Version != "fake" {
		t.Fatal(v, e)
	}
	if e := api.Pull(context.Background(), image(t)); e == nil {
		t.Fatal("unconfigured fake succeeded")
	}
}

func TestSpecCompatibleReferences(t *testing.T) {
	for _, ref := range []string{"registry.example:5000/team/a__b--c:release@sha256:" + strings.Repeat("a", 64), "registry.example/app@sha256:" + strings.Repeat("A", 64)} {
		v, e := ParseImage(ref)
		if e != nil || !strings.HasSuffix(v.String(), strings.Repeat("a", 64)) {
			t.Fatalf("reference rejected: %v", e)
		}
	}
	if _, e := ParseName("a" + strings.Repeat("b", 252)); e != nil {
		t.Fatal(e)
	}
}
func TestSecretSizeBoundary(t *testing.T) {
	r := &recorder{}
	c := client(t, r)
	for _, data := range [][]byte{nil, {}, make([]byte, 512000)} {
		if e := c.CreateSecret(context.Background(), name(t), data); e == nil {
			t.Fatal("invalid secret accepted")
		}
	}
	if len(r.commands) != 0 {
		t.Fatal("invalid secret executed")
	}
}

func TestInspectRejectsUnboundOrAmbiguousManifest(t *testing.T) {
	inspect := fixture(t, "image-inspect.json")
	manifest := fixture(t, "manifest-inspect.json")
	tests := []struct{ inspect, manifest string }{
		{"[]", manifest},
		{strings.ReplaceAll(inspect, strings.Repeat("a", 64), strings.Repeat("d", 64)), manifest},
		{inspect, `{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`},
		{inspect, strings.ReplaceAll(manifest, strings.Repeat("b", 64), "invalid")},
		{inspect, "private-secret"},
		{inspect, `{"mediaType":"application/vnd.oci.image.manifest.v1+json"}`},
	}
	for _, tt := range tests {
		r := &recorder{results: []localexec.Result{{}, {Stdout: tt.inspect}, {Stdout: tt.manifest}}}
		_, e := client(t, r).Inspect(context.Background(), image(t))
		if e == nil || strings.Contains(e.Error(), "private-secret") {
			t.Fatalf("unbound manifest accepted: %v", e)
		}
	}
}
func TestMissingObjectsAndFailedContainerProbe(t *testing.T) {
	for _, code := range []int{1, 125} {
		r := &recorder{err: &localexec.Error{Kind: localexec.Failed, ExitCode: code}}
		_, e := client(t, r).ContainerState(context.Background(), name(t))
		var re *localexec.Error
		want := localexec.Failed
		if code == 1 {
			want = localexec.NotFound
		}
		if !errors.As(e, &re) || re.Kind != want {
			t.Fatal(e)
		}
		r = &recorder{err: &localexec.Error{Kind: localexec.Failed, ExitCode: code}}
		_, e = client(t, r).Inspect(context.Background(), image(t))
		if !errors.As(e, &re) || re.Kind != want {
			t.Fatal(e)
		}
	}
}
func TestSecretListParsingAndError(t *testing.T) {
	for _, out := range []string{"--invalid\n", "{}", "private secret\n", "a\n\nb"} {
		r := &recorder{results: []localexec.Result{{Stdout: out}}}
		if _, e := client(t, r).SecretNames(context.Background()); e == nil {
			t.Fatal("malformed list accepted")
		}
	}
	r := &recorder{results: []localexec.Result{{Stdout: ""}}}
	names, e := client(t, r).SecretNames(context.Background())
	if e != nil || names == nil || len(names) != 0 {
		t.Fatal(names, e)
	}
}
func TestAllFakeOperations(t *testing.T) {
	ctx := context.Background()
	i := image(t)
	n := name(t)
	calls := 0
	f := &Fake{
		VersionFunc:     func(context.Context) (Version, error) { calls++; return Version{}, nil },
		PullFunc:        func(context.Context, Image) error { calls++; return nil },
		InspectFunc:     func(context.Context, Image) (ImageInfo, error) { calls++; return ImageInfo{}, nil },
		ImageExistsFunc: func(context.Context, Image) (bool, error) { calls++; return true, nil },
		CreateSecretFunc: func(_ context.Context, _ Name, b []byte) error {
			calls++
			if string(b) != "secret" {
				t.Fatal("fake lost secret")
			}
			return nil
		},
		SecretExistsFunc:   func(context.Context, Name) (bool, error) { calls++; return true, nil },
		SecretNamesFunc:    func(context.Context) ([]Name, error) { calls++; return []Name{n}, nil },
		ContainerStateFunc: func(context.Context, Name) (ContainerState, error) { calls++; return ContainerState{}, nil },
	}
	f.Version(ctx)
	f.Pull(ctx, i)
	f.Inspect(ctx, i)
	f.ImageExists(ctx, i)
	f.CreateSecret(ctx, n, []byte("secret"))
	f.SecretExists(ctx, n)
	f.SecretNames(ctx)
	f.ContainerState(ctx, n)
	if calls != 8 {
		t.Fatal(calls)
	}
	empty := &Fake{}
	_, a := empty.Version(ctx)
	b := empty.Pull(ctx, i)
	_, c := empty.Inspect(ctx, i)
	_, d := empty.ImageExists(ctx, i)
	e := empty.CreateSecret(ctx, n, []byte("s"))
	_, g := empty.SecretExists(ctx, n)
	_, h := empty.SecretNames(ctx)
	_, j := empty.ContainerState(ctx, n)
	for _, err := range []error{a, b, c, d, e, g, h, j} {
		if err == nil {
			t.Fatal("unconfigured fake succeeded")
		}
	}
}

func TestStoredPrimaryDigestDoesNotOverrideAssociatedIndex(t *testing.T) {
	r := &recorder{results: []localexec.Result{{}, {Stdout: fixture(t, "platform-image-inspect.json")}, {Stdout: fixture(t, "manifest-inspect.json")}, {Stdout: fixture(t, "platform-image-inspect.json")}}}
	got, e := client(t, r).Inspect(context.Background(), image(t))
	if e != nil || got.IndexDigest == got.ManifestDigest {
		t.Fatalf("associated index rejected: %#v %v", got, e)
	}
}
func TestRealOCIPlatformPin(t *testing.T) {
	pin, e := ParseImage("registry.example/app@sha256:" + strings.Repeat("b", 64))
	if e != nil {
		t.Fatal(e)
	}
	code, err := strconv.Atoi(strings.TrimSpace(fixture(t, "platform-manifest.exit")))
	if err != nil {
		t.Fatal(err)
	}
	r := &recorder{results: []localexec.Result{{}, {Stdout: fixture(t, "platform-image-inspect.json")}, {Stdout: fixture(t, "platform-manifest.stdout"), Stderr: fixture(t, "platform-manifest.stderr")}}, errors: []error{nil, nil, &localexec.Error{Kind: localexec.Failed, ExitCode: code}}}
	got, e := client(t, r).Inspect(context.Background(), pin)
	if e != nil || got.ManifestDigest != got.IndexDigest || got.ManifestDigest != pin.String()[len("registry.example/app@"):] {
		t.Fatalf("OCI single pin = %#v %v", got, e)
	}
}

func TestDockerSchema2SingleUsesLocalLookupNotMissingConfig(t *testing.T) {
	pin, _ := ParseImage("registry.example/app@sha256:" + strings.Repeat("b", 64))
	local := strings.ReplaceAll(fixture(t, "platform-image-inspect.json"), ociManifest, dockerManifest)
	// Source-derived Docker schema-2 output. The one live image uses OCI.
	r := &recorder{results: []localexec.Result{{}, {Stdout: local}, {Stdout: `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","manifests":null}`}}}
	got, e := client(t, r).Inspect(context.Background(), pin)
	if e != nil || got.ManifestDigest != pin.digest() {
		t.Fatalf("schema2 = %#v %v", got, e)
	}
}
func TestSinglePinNeverHidesRealRuntimeFailures(t *testing.T) {
	pin, _ := ParseImage("registry.example/app@sha256:" + strings.Repeat("b", 64))
	for _, tt := range []struct {
		err       error
		stderr    string
		truncated bool
	}{{&localexec.Error{Kind: localexec.Failed, ExitCode: 125}, "private-secret", false}, {&localexec.Error{Kind: localexec.Timeout}, fixture(t, "platform-manifest.stderr"), false}, {&localexec.Error{Kind: localexec.Failed, ExitCode: 125}, fixture(t, "platform-manifest.stderr"), true}} {
		r := &recorder{results: []localexec.Result{{}, {Stdout: fixture(t, "platform-image-inspect.json")}, {Stderr: tt.stderr, Truncated: tt.truncated}}, errors: []error{nil, nil, tt.err}}
		_, e := client(t, r).Inspect(context.Background(), pin)
		if e == nil || strings.Contains(e.Error(), "private-secret") {
			t.Fatalf("runtime failure hidden: %v", e)
		}
	}
}
func TestSelectedManifestMustResolveToSameLocalImage(t *testing.T) {
	var rows []map[string]any
	if e := json.Unmarshal([]byte(fixture(t, "platform-image-inspect.json")), &rows); e != nil {
		t.Fatal(e)
	}
	rows[0]["Id"] = strings.Repeat("c", 64)
	b, _ := json.Marshal(rows)
	r := &recorder{results: []localexec.Result{{}, {Stdout: fixture(t, "image-inspect.json")}, {Stdout: fixture(t, "manifest-inspect.json")}, {Stdout: string(b)}}}
	if _, e := client(t, r).Inspect(context.Background(), image(t)); e == nil {
		t.Fatal("different selected image ID accepted")
	}
}
func TestAssociatedDigestDisambiguatesSameArchitecture(t *testing.T) {
	manifest := strings.ReplaceAll(fixture(t, "manifest-inspect.json"), "amd64", "arm64")
	r := &recorder{results: []localexec.Result{{}, {Stdout: fixture(t, "image-inspect.json")}, {Stdout: manifest}, {Stdout: fixture(t, "platform-image-inspect.json")}}}
	got, e := client(t, r).Inspect(context.Background(), image(t))
	if e != nil || got.ManifestDigest != "sha256:"+strings.Repeat("b", 64) {
		t.Fatal(got, e)
	}
}
func TestMultipleLocallyAssociatedCandidatesFailClosed(t *testing.T) {
	manifest := `{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"sha256:` + strings.Repeat("b", 64) + `","platform":{"os":"linux","architecture":"arm64","variant":"v8"}},{"digest":"sha256:` + strings.Repeat("b", 64) + `","platform":{"os":"linux","architecture":"arm64","variant":"v9"}}]}`
	r := &recorder{results: []localexec.Result{{}, {Stdout: fixture(t, "image-inspect.json")}, {Stdout: manifest}, {Stdout: fixture(t, "platform-image-inspect.json")}, {Stdout: fixture(t, "platform-image-inspect.json")}}}
	if _, e := client(t, r).Inspect(context.Background(), image(t)); e == nil {
		t.Fatal("ambiguous local candidates accepted")
	}
}

func TestInspectCanonicalDockerHubRepositories(t *testing.T) {
	pin := "@sha256:" + strings.Repeat("a", 64)
	for _, repository := range []string{"docker.io/alpine", "index.docker.io/alpine", "docker.io/library/alpine", "index.docker.io/library/alpine"} {
		t.Run(repository, func(t *testing.T) {
			ref, err := ParseImage(repository + pin)
			if err != nil {
				t.Fatal(err)
			}
			metadata := strings.ReplaceAll(fixture(t, "platform-image-inspect.json"), "registry.example/app", "docker.io/library/alpine")
			r := &recorder{results: []localexec.Result{{}, {Stdout: metadata}, {Stdout: fixture(t, "manifest-inspect.json")}, {Stdout: metadata}}}
			info, err := client(t, r).Inspect(context.Background(), ref)
			if err != nil || info.IndexDigest != "sha256:"+strings.Repeat("a", 64) || info.ManifestDigest != "sha256:"+strings.Repeat("b", 64) {
				t.Fatalf("inspection = %#v, %v", info, err)
			}
		})
	}
}

func TestAssociatedRepositoryNormalizationPreservesBinding(t *testing.T) {
	pin := "@sha256:" + strings.Repeat("a", 64)
	for _, tt := range []struct {
		requested, stored string
		want              bool
	}{
		{"docker.io/alpine", "index.docker.io/library/alpine", true},
		{"library/alpine", "docker.io/library/alpine", true},
		{"team/alpine", "docker.io/team/alpine", true},
		{"docker.io/team/alpine", "index.docker.io/team/alpine", true},
		{"localhost/alpine", "docker.io/library/alpine", false},
		{"docker.io:5000/alpine", "docker.io/library/alpine", false},
		{"docker.io/team/alpine", "docker.io/library/alpine", false},
		{"registry.example/alpine", "docker.io/library/alpine", false},
	} {
		t.Run(tt.requested+"/"+tt.stored, func(t *testing.T) {
			ref, err := ParseImage(tt.requested + pin)
			if err != nil {
				t.Fatal(err)
			}
			row := localImage{RepoDigests: []string{tt.stored + pin}}
			if row.associates(ref) != tt.want {
				t.Fatal("incorrect repository binding")
			}
			row.RepoDigests = []string{tt.stored + "@sha256:" + strings.Repeat("b", 64)}
			if row.associates(ref) {
				t.Fatal("different digest bound")
			}
		})
	}
}

func TestImmutableSecretNameGrammar(t *testing.T) {
	for _, name := range []string{"brine.hello.token.v1", "brine.hello-team.team-token.v18446744073709551615"} {
		if _, err := ParseSecretName(name); err != nil {
			t.Fatal(name, err)
		}
	}
	for _, name := range []string{"brine-hello-token-v1", "brine.hello.token.with-dot.v1", "brine.hello.token.v0", "brine.hello.token.v01", "brine.hello.token.v18446744073709551616"} {
		if _, err := ParseSecretName(name); err == nil {
			t.Fatal("invalid secret name accepted", name)
		}
	}
}

func TestInspectStoredAcceptsAssociatedIndexWithPrimaryManifest(t *testing.T) {
	platform, _ := ParseImage("registry.example/app@sha256:" + strings.Repeat("b", 64))
	r := &recorder{results: []localexec.Result{{Stdout: fixture(t, "platform-image-inspect.json")}, {Stdout: fixture(t, "platform-image-inspect.json")}}}
	got, err := client(t, r).InspectStored(context.Background(), image(t), platform)
	if err != nil || got.IndexDigest != image(t).digest() || got.ManifestDigest != platform.digest() || got.Platform.Architecture != "arm64" {
		t.Fatal("stored index alias rejected", got, err)
	}
	for _, cmd := range r.commands {
		if cmd.Args[0] == "manifest" {
			t.Fatal("associated local aliases required registry", cmd.Args)
		}
	}
}

func TestInspectStoredAliasLayoutsAndRefusals(t *testing.T) {
	index := image(t)
	platform, _ := ParseImage("registry.example/app@sha256:" + strings.Repeat("b", 64))
	layout := func(refs []string, digest string) string {
		var rows []map[string]any
		if err := json.Unmarshal([]byte(fixture(t, "platform-image-inspect.json")), &rows); err != nil {
			t.Fatal(err)
		}
		rows[0]["RepoDigests"] = refs
		rows[0]["Digest"] = digest
		raw, err := json.Marshal(rows)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	onlyManifest := layout([]string{platform.String()}, platform.digest())
	onlyIndex := layout([]string{index.String()}, platform.digest())
	primaryIndex := layout([]string{index.String(), platform.String()}, index.digest())
	foreign := strings.ReplaceAll(fixture(t, "platform-image-inspect.json"), "registry.example/app", "registry.example/foreign")
	for _, tc := range []struct {
		name, indexed, selected, list string
		known                         bool
	}{
		{"only selected manifest association with index list", onlyManifest, onlyManifest, fixture(t, "manifest-inspect.json"), true},
		{"only index association with primary platform digest", onlyIndex, onlyIndex, "", true},
		{"primary index on platform alias", primaryIndex, primaryIndex, "", true},
		{"unrelated repository", foreign, foreign, "", false},
		{"different stored identity", fixture(t, "platform-image-inspect.json"), strings.ReplaceAll(fixture(t, "platform-image-inspect.json"), "748902c9f9368aa7437b05e353c23968266b0bc882ac1d74067fc1768a102ba6", strings.Repeat("f", 64)), "", false},
		{"different platform", fixture(t, "platform-image-inspect.json"), strings.ReplaceAll(fixture(t, "platform-image-inspect.json"), "arm64", "amd64"), "", false},
		{"unresolved index without index association", onlyManifest, onlyManifest, `{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`, false},
		{"index list selects unrelated manifest", onlyManifest, onlyManifest, strings.ReplaceAll(fixture(t, "manifest-inspect.json"), strings.Repeat("b", 64), strings.Repeat("e", 64)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{results: []localexec.Result{{Stdout: tc.indexed}, {Stdout: tc.selected}, {Stdout: tc.list}, {Stdout: tc.selected}}}
			got, err := client(t, r).InspectStored(context.Background(), index, platform)
			if (err == nil) != tc.known {
				t.Fatal(got, err)
			}
			if tc.known && (got.IndexDigest != index.digest() || got.ManifestDigest != platform.digest() || got.Platform.Architecture != "arm64") {
				t.Fatal(got)
			}
		})
	}
}
