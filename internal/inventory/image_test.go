package inventory

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

type imageRunner struct {
	fakeRunner
	results map[string]string
}

func (r imageRunner) Execute(_ context.Context, cmd localexec.Command) (localexec.Result, error) {
	if cmd.Mutation || len(cmd.Stdin) > 0 {
		return localexec.Result{}, fmt.Errorf("mutation refused")
	}
	out, ok := r.results[strings.Join(cmd.Args, " ")]
	if !ok {
		return localexec.Result{}, &localexec.Error{Kind: localexec.Failed, ExitCode: 1}
	}
	return localexec.Result{Stdout: out}, nil
}

func TestInstalledImageObservationAndPlanning(t *testing.T) {
	read := func(path string) []byte {
		t.Helper()
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	const repository = "registry.example/app@"
	index := "sha256:" + strings.Repeat("a", 64)
	manifest := "sha256:" + strings.Repeat("b", 64)
	imageID := "748902c9f9368aa7437b05e353c23968266b0bc882ac1d74067fc1768a102ba6"
	for _, tc := range []struct {
		name       string
		change     func(map[string]string)
		known      bool
		unitChange func(string) string
	}{
		{"matching", nil, true, nil},
		{"different container image", func(m map[string]string) {
			m["container inspect systemd-hello"] = strings.ReplaceAll(m["container inspect systemd-hello"], imageID, strings.Repeat("f", 64))
		}, false, nil},
		{"missing container", func(m map[string]string) { delete(m, "container exists systemd-hello") }, false, nil},
		{"unreadable image", func(m map[string]string) { delete(m, "image inspect "+repository+index) }, false, nil},
		{"unreadable manifest", func(m map[string]string) { delete(m, "manifest inspect "+repository+index) }, false, nil},
		{"stopped", func(m map[string]string) {
			m["container inspect systemd-hello"] = strings.ReplaceAll(m["container inspect systemd-hello"], `"Running":true`, `"Running":false`)
		}, false, nil},
		{"unit pin mismatch", nil, false, func(s string) string {
			return strings.ReplaceAll(s, "Image="+repository+manifest, "Image="+repository+index)
		}},
		{"unit platform mismatch", nil, false, func(s string) string {
			return strings.ReplaceAll(s, "# Platform=linux/arm64", "# Platform=linux/amd64")
		}},
		{"missing index", nil, false, func(s string) string { return strings.ReplaceAll(s, "# IndexDigest="+index+"\n", "") }},
		{"duplicate pin", nil, false, func(s string) string { return s + "Image=" + repository + manifest + "\n" }},
		{"custom container name", nil, false, func(s string) string { return s + "ContainerName=foreign\n" }},
		{"unbound index", nil, false, func(s string) string {
			return strings.ReplaceAll(s, "# IndexDigest="+index, "# IndexDigest=sha256:"+strings.Repeat("e", 64))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := secretOnlyFixture()
			dir := "/home/brine/.config/containers/systemd"
			f.dirs[dir] = []fs.DirEntry{fixtureEntry("hello.container")}
			f.files[dir+"/hello.container"] = "# Brine-owned plan=" + index + "\n# IndexDigest=" + index + "\n# PlatformManifestDigest=" + manifest + "\n# Platform=linux/arm64\n\n[Container]\nImage=" + repository + manifest + "\n"
			if tc.unitChange != nil {
				f.files[dir+"/hello.container"] = tc.unitChange(f.files[dir+"/hello.container"])
			}
			results := map[string]string{
				"container exists systemd-hello":         "",
				"container inspect systemd-hello":        `[{"Name":"systemd-hello","Image":"` + imageID + `","Config":{"Labels":{"PODMAN_SYSTEMD_UNIT":"hello.service"}},"State":{"Status":"running","Running":true}}]`,
				"image exists " + repository + index:     "",
				"image inspect " + repository + index:    string(read("../podman/testdata/image-inspect.json")),
				"manifest inspect " + repository + index: string(read("../podman/testdata/manifest-inspect.json")),
				"image inspect " + repository + manifest: string(read("../podman/testdata/platform-image-inspect.json")),
			}
			if tc.change != nil {
				tc.change(results)
			}
			r := imageRunner{fakeRunner: fakeRunner{
				"podman --remote=false secret ls --format {{.ID}} {{.Name}}":                                      "",
				"podman --remote=false inspect --type container --format " + runtimePortFormat + " systemd-hello": `{"name":"systemd-hello","running":true,"unit":"hello.service","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"20000"}]}}`,
			}, results: results}
			snapshot, e := target.Decode(read("../target/testdata/one-app.json"))
			if e != nil {
				t.Fatal(e)
			}
			(Collector{FS: f, Runner: r, RunnerUser: "brine"}).apps(context.Background(), &snapshot, "/home/brine", true)
			app := (*snapshot.Apps.Value)[0]
			if (app.Image.Status == target.KnownStatus) != tc.known {
				t.Fatalf("image=%+v", app.Image)
			}
			if tc.known && (app.Image.Value.Digest != index || app.Image.Value.Platform.Arch != "arm64") {
				t.Fatalf("image=%+v", app.Image)
			}
			operator, e := policy.Parse(bytes.ReplaceAll(read("../policy/testdata/operator.toml"), []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
			if e != nil {
				t.Fatal(e)
			}
			parsed, e := spec.Parse(bytes.ReplaceAll(read("../spec/testdata/valid-minimal.toml"), []byte("example/hello"), []byte("team/hello")))
			if e != nil {
				t.Fatal(e)
			}
			desired, e := policy.Normalize(parsed, operator)
			if e != nil {
				t.Fatal(e)
			}
			plannedImage := plan.Image{Digest: index, Platform: target.Platform{OS: "linux", Arch: "arm64"}, ManifestDigest: target.Known(manifest)}
			state := plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{{App: "hello", ID: "release-0001", Desired: desired, Image: plannedImage, HostPort: 20000, Secrets: []plan.SecretBinding{}, Units: *app.QuadletUnits.Value, CaddyFile: snapshot.CaddyConfig.Value.Files[0]}}}
			in := plan.Input{Desired: desired, Snapshot: snapshot, Image: plannedImage, State: state}
			p, e := plan.Build(in)
			if e != nil {
				t.Fatal(e)
			}
			if !tc.known {
				if p.Kind != plan.Conflict || len(p.Changes) != 0 {
					t.Fatalf("unsafe plan=%+v", p)
				}
				found := false
				for _, d := range p.Conflicts {
					if d.Field == "app.image" && d.Code == plan.UnknownFacts {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing image conflict=%+v", p.Conflicts)
				}
				return
			}
			if p.Kind != plan.NoOp {
				t.Fatalf("no-op=%+v", p)
			}
			in.Desired.Environment = []policy.Environment{{Name: "APP_ENV", Value: "production"}}
			p, e = plan.Build(in)
			if e != nil || p.Kind != plan.Update {
				t.Fatalf("update=%+v error=%v", p, e)
			}
			in.Desired.Environment = desired.Environment
			in.Desired.Image = spec.ImageReference(strings.ReplaceAll(string(desired.Image), index, "sha256:"+strings.Repeat("e", 64)))
			in.Image.Digest = "sha256:" + strings.Repeat("e", 64)
			in.Image.ManifestDigest = target.Known("sha256:" + strings.Repeat("f", 64))
			p, e = plan.Build(in)
			if e != nil || p.Kind != plan.Update {
				t.Fatalf("image update=%+v error=%v", p, e)
			}
		})
	}
}
