//go:build linux

package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

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
	// Model a registry read longer than the three-second local-probe budget.
	if len(cmd.Args) > 0 && cmd.Args[0] == "manifest" && cmd.Timeout < 4*time.Second {
		return localexec.Result{}, &localexec.Error{Kind: localexec.Timeout}
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
		inactive   string
	}{
		{"matching", nil, true, nil, ""},
		{"different container image", func(m map[string]string) {
			m["container inspect systemd-hello"] = strings.ReplaceAll(m["container inspect systemd-hello"], imageID, strings.Repeat("f", 64))
		}, false, nil, ""},
		{"missing container", func(m map[string]string) { delete(m, "container exists systemd-hello") }, false, nil, ""},
		{"unreadable image", func(m map[string]string) { delete(m, "image inspect "+repository+index) }, false, nil, ""},
		{"unreadable manifest", func(m map[string]string) { delete(m, "manifest inspect "+repository+index) }, false, nil, ""},
		{"stopped", func(m map[string]string) {
			m["container inspect systemd-hello"] = strings.ReplaceAll(m["container inspect systemd-hello"], `"Running":true`, `"Running":false`)
		}, false, nil, ""},
		{"unit pin mismatch", nil, false, func(s string) string {
			return strings.ReplaceAll(s, "Image="+repository+manifest, "Image="+repository+index)
		}, ""},
		{"unit platform mismatch", nil, false, func(s string) string {
			return strings.ReplaceAll(s, "# Platform=linux/arm64", "# Platform=linux/amd64")
		}, ""},
		{"missing index", nil, false, func(s string) string { return strings.ReplaceAll(s, "# IndexDigest="+index+"\n", "") }, ""},
		{"duplicate pin", nil, false, func(s string) string { return s + "Image=" + repository + manifest + "\n" }, ""},
		{"custom container name", nil, false, func(s string) string { return s + "ContainerName=foreign\n" }, ""},
		{"unbound index", nil, false, func(s string) string {
			return strings.ReplaceAll(s, "# IndexDigest="+index, "# IndexDigest=sha256:"+strings.Repeat("e", 64))
		}, ""},
		{"inactive stored image", func(m map[string]string) {
			m["container inspect systemd-hello"] = strings.ReplaceAll(strings.ReplaceAll(m["container inspect systemd-hello"], `"Running":true`, `"Running":false`), `"Status":"running"`, `"Status":"exited"`)
			delete(m, "manifest inspect "+repository+index)
		}, true, nil, "inactive"},
		{"inactive multi-platform primary manifest alias", func(m map[string]string) {
			m["container inspect systemd-hello"] = strings.ReplaceAll(strings.ReplaceAll(m["container inspect systemd-hello"], `"Running":true`, `"Running":false`), `"Status":"running"`, `"Status":"exited"`)
			m["image inspect "+repository+index] = m["image inspect "+repository+manifest]
			delete(m, "manifest inspect "+repository+index)
		}, true, nil, "inactive"},

		{"inactive multi-platform index resolved by manifest list", func(m map[string]string) {
			m["container inspect systemd-hello"] = strings.ReplaceAll(strings.ReplaceAll(m["container inspect systemd-hello"], `"Running":true`, `"Running":false`), `"Status":"running"`, `"Status":"exited"`)
			var rows []map[string]any
			if err := json.Unmarshal([]byte(m["image inspect "+repository+manifest]), &rows); err != nil {
				t.Fatal(err)
			}
			rows[0]["RepoDigests"] = []string{repository + manifest}
			raw, err := json.Marshal(rows)
			if err != nil {
				t.Fatal(err)
			}
			m["image inspect "+repository+index] = string(raw)
			m["image inspect "+repository+manifest] = string(raw)
		}, true, nil, "inactive"},

		{"inactive but running container", nil, false, nil, "inactive"},
		{"inactive missing container", func(m map[string]string) {
			delete(m, "container exists systemd-hello")
			delete(m, "manifest inspect "+repository+index)
		}, true, nil, "inactive"},
		{"inactive wrong stored ID", func(m map[string]string) {
			m["image inspect "+repository+manifest] = strings.ReplaceAll(m["image inspect "+repository+manifest], imageID, strings.Repeat("f", 64))
		}, false, nil, "inactive"},
		{"inactive platform lookup with primary index alias", func(m map[string]string) {
			m["image inspect "+repository+manifest] = strings.ReplaceAll(m["image inspect "+repository+manifest], `"Digest": "`+manifest, `"Digest": "`+index)
			m["container inspect systemd-hello"] = strings.ReplaceAll(strings.ReplaceAll(m["container inspect systemd-hello"], `"Running":true`, `"Running":false`), `"Status":"running"`, `"Status":"exited"`)
		}, true, nil, "inactive"},
		{"inactive foreign container", func(m map[string]string) {
			m["container inspect systemd-hello"] = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(m["container inspect systemd-hello"], `"Running":true`, `"Running":false`), `"Status":"running"`, `"Status":"exited"`), `"hello.service"`, `"foreign.service"`)
		}, false, nil, "inactive"},
		{"deactivating running container", nil, false, nil, "deactivating"},

		{"inactive missing local manifest", func(m map[string]string) { delete(m, "image inspect "+repository+manifest) }, false, nil, "inactive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := secretOnlyFixture()
			dir := "/home/brine/.config/containers/systemd"
			f.dirs[dir] = []fs.DirEntry{fixtureEntry("hello.container")}
			f.files[dir+"/hello.container"] = "# Brine-owned plan=" + index + "\n# IndexDigest=" + index + "\n# PlatformManifestDigest=" + manifest + "\n# Platform=linux/arm64\n\n[Container]\nImage=" + repository + manifest + "\nPublishPort=127.0.0.1:20000:8080\n"
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
			if tc.inactive != "" {
				r.fakeRunner["systemctl --user show hello.service --property=ActiveState --value"] = tc.inactive
				r.fakeRunner["podman --remote=false inspect --type container --format "+runtimePortFormat+" systemd-hello"] = `{"name":"systemd-hello","running":false,"unit":"hello.service","ports":{}}`
			}
			snapshot, e := target.Decode(read("../target/testdata/one-app.json"))
			if e != nil {
				t.Fatal(e)
			}
			if tc.inactive != "" {
				snapshot.UsedPorts = target.Known([]target.Port{})
				snapshot.PortOwners = target.Known([]target.PortOwner{})
			}
			collector := Collector{FS: f, Runner: r, RunnerUser: "brine"}
			artifacts := collector.apps(context.Background(), &snapshot, "/home/brine", true, nil)
			collector.images(context.Background(), &snapshot, "/home/brine", artifacts)
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
			if tc.inactive != "" {
				running := in
				raw, err := target.Encode(in.Snapshot)
				if err != nil {
					t.Fatal(err)
				}
				running.Snapshot, err = target.Decode(raw)
				if err != nil {
					t.Fatal(err)
				}
				unitActive := target.Known(true)
				(*running.Snapshot.Apps.Value)[0].UnitActive = &unitActive
				running.Snapshot.UsedPorts = target.Known([]target.Port{20000})
				running.Snapshot.PortOwners = target.Known([]target.PortOwner{{Port: 20000, App: "hello"}})
				firstStop, err := plan.BuildLifecycle(running, plan.StopApp)
				if err != nil || firstStop.Kind != plan.Update {
					t.Fatal("first stop refused", firstStop, err)
				}

				if app.AllocatedHostPort.Status != target.KnownStatus || *app.AllocatedHostPort.Value != 20000 {
					t.Fatal("stopped port unknown", app.AllocatedHostPort)
				}
				for _, action := range []plan.ChangeKind{plan.StartApp, plan.RestartApp, plan.StopApp} {
					lifecycle, err := plan.BuildLifecycle(in, action)
					if err != nil || lifecycle.Kind != plan.Update {
						t.Fatal("stopped lifecycle refused", action, lifecycle.Conflicts, err)
					}
				}
				foreign := in
				foreign.Snapshot.UsedPorts = target.Known([]target.Port{20000})
				foreign.Snapshot.PortOwners = target.Known([]target.PortOwner{{Port: 20000, App: "foreign"}})
				refused, err := plan.BuildLifecycle(foreign, plan.StartApp)
				if err != nil || refused.Kind != plan.Conflict {
					t.Fatal("foreign listener accepted", refused, err)
				}
				foreign.Snapshot.PortOwners = target.Known([]target.PortOwner{{Port: 20000, App: "hello"}})
				refused, err = plan.BuildLifecycle(foreign, plan.StartApp)
				if err != nil || refused.Kind != plan.Conflict {
					t.Fatal("contradictory stopped listener accepted", refused, err)
				}

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

type stalledImagesRunner struct {
	imageRunner
	manifestCalls int
}

func (r *stalledImagesRunner) CaptureStdout(ctx context.Context, limit int, p string, args ...string) (localexec.Capture, error) {
	if err := ctx.Err(); err != nil {
		return localexec.Capture{}, err
	}
	return r.fakeRunner.CaptureStdout(ctx, limit, p, args...)
}
func (r *stalledImagesRunner) Execute(ctx context.Context, cmd localexec.Command) (localexec.Result, error) {
	if len(cmd.Args) > 0 && cmd.Args[0] == "manifest" {
		r.manifestCalls++
		<-ctx.Done()
		return localexec.Result{}, &localexec.Error{Kind: localexec.Timeout}
	}
	if err := ctx.Err(); err != nil {
		return localexec.Result{}, err
	}
	return r.imageRunner.Execute(ctx, cmd)
}

func TestUnavailableImagesDoNotExhaustCollection(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile("../podman/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	f := secretOnlyFixture()
	dir := "/home/brine/.config/containers/systemd"
	f.dirs[dir] = []fs.DirEntry{}
	index := "sha256:" + strings.Repeat("a", 64)
	manifest := "sha256:" + strings.Repeat("b", 64)
	r := &stalledImagesRunner{imageRunner: imageRunner{fakeRunner: fakeRunner{"uname -m": "aarch64", "ss -H -ltnpe": "", "ss -H -lunp": "", "podman --remote=false secret ls --format {{.ID}} {{.Name}}": ""}, results: map[string]string{"image exists registry.example/app@" + index: "", "image inspect registry.example/app@" + index: read("image-inspect.json")}}}
	for i, name := range []string{"api", "worker", "web"} {
		f.dirs[dir] = append(f.dirs[dir], fixtureEntry(name+".container"))
		f.files[dir+"/"+name+".container"] = "# Brine-owned plan=" + index + "\n# IndexDigest=" + index + "\n# PlatformManifestDigest=" + manifest + "\n# Platform=linux/arm64\n[Container]\nImage=registry.example/app@" + manifest + "\n"
		r.results["container exists systemd-"+name] = ""
		r.results["container inspect systemd-"+name] = `[{"Name":"systemd-` + name + `","Image":"748902c9f9368aa7437b05e353c23968266b0bc882ac1d74067fc1768a102ba6","Config":{"Labels":{"PODMAN_SYSTEMD_UNIT":"` + name + `.service"}},"State":{"Status":"running","Running":true}}]`
		r.fakeRunner["podman --remote=false inspect --type container --format "+runtimePortFormat+" systemd-"+name] = fmt.Sprintf(`{"name":"systemd-%s","running":true,"unit":"%s.service","ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"%d"}]}}`, name, name, 20080+i)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := (Collector{FS: f, Runner: r, IdentityKey: []byte("fixture")}).Collect(ctx)
	if err != nil {
		t.Fatalf("image probes exhausted inventory: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("caller deadline consumed")
	}
	if r.manifestCalls != 3 {
		t.Fatalf("fair image slices attempted %d apps, want 3", r.manifestCalls)
	}
	if s.Apps.Value == nil || len(*s.Apps.Value) != 3 || s.PortOwners.Status != target.KnownStatus || s.UsedPorts.Status != target.KnownStatus {
		t.Fatalf("required facts lost: %+v", s)
	}
	for _, app := range *s.Apps.Value {
		if app.Image.Status != target.Unknown || app.AllocatedHostPort.Status != target.KnownStatus {
			t.Fatalf("image failure lost unrelated app facts: %+v", app)
		}
	}
}
