package podman

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestRunningContainerImage(t *testing.T) {
	for _, tc := range []struct {
		name, image, unit string
		running, known    bool
	}{
		{"matching", "748902c9f9368aa7437b05e353c23968266b0bc882ac1d74067fc1768a102ba6", "api.service", true, true},
		{"different image", strings.Repeat("f", 64), "api.service", true, false},
		{"wrong unit", "748902c9f9368aa7437b05e353c23968266b0bc882ac1d74067fc1768a102ba6", "other.service", true, false},
		{"stopped", "748902c9f9368aa7437b05e353c23968266b0bc882ac1d74067fc1768a102ba6", "api.service", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			running := "false"
			if tc.running {
				running = "true"
			}
			container := `[{"Name":"systemd-api","Image":"` + tc.image + `","Config":{"Labels":{"PODMAN_SYSTEMD_UNIT":"` + tc.unit + `"}},"State":{"Status":"running","Running":` + running + `}}]`
			r := &recorder{results: []localexec.Result{{}, {Stdout: container}, {Stdout: fixture(t, "image-inspect.json")}, {Stdout: fixture(t, "platform-image-inspect.json")}}}
			n, _ := ParseName("systemd-api")
			manifest, _ := ParseImage("registry.example/app@sha256:" + strings.Repeat("b", 64))
			got, err := client(t, r).RunningContainerImage(context.Background(), n, "api.service", image(t), manifest)
			if (err == nil) != tc.known {
				t.Fatalf("info=%+v error=%v", got, err)
			}
			if tc.known && (got.ManifestDigest != "sha256:"+strings.Repeat("b", 64) || got.ImageID != tc.image) {
				t.Fatalf("info=%+v", got)
			}
			for _, cmd := range r.commands {
				if cmd.Mutation || cmd.Args[0] == "manifest" {
					t.Fatal("inventory mutated runtime or requested a registry manifest")
				}
			}
		})
	}
}

func TestRunningContainerImageLocalPinShapes(t *testing.T) {
	manifest, _ := ParseImage("registry.example/app@sha256:" + strings.Repeat("b", 64))
	for _, tc := range []struct {
		name  string
		index Image
	}{
		{"single manifest", manifest},
		{"index and selected manifest", image(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := fixture(t, "platform-image-inspect.json")
			container := `[{"Name":"systemd-api","Image":"748902c9f9368aa7437b05e353c23968266b0bc882ac1d74067fc1768a102ba6","Config":{"Labels":{"PODMAN_SYSTEMD_UNIT":"api.service"}},"State":{"Status":"running","Running":true}}]`
			r := &recorder{results: []localexec.Result{{}, {Stdout: container}, {Stdout: local}, {Stdout: local}}}
			name, _ := ParseName("systemd-api")
			got, err := client(t, r).RunningContainerImage(context.Background(), name, "api.service", tc.index, manifest)
			if err != nil || got.IndexDigest != tc.index.digest() || got.ManifestDigest != manifest.digest() || got.Platform.Architecture != "arm64" {
				t.Fatal(got, err)
			}
			if len(r.commands) != 4 {
				t.Fatal("unexpected probe count", len(r.commands))
			}
			for _, cmd := range r.commands {
				if cmd.Mutation || cmd.Args[0] == "manifest" {
					t.Fatal("local pins required a registry request or mutation", cmd.Args)
				}
			}
		})
	}
}

func TestRunningContainerImageMissingIndexAssociationStaysLocal(t *testing.T) {
	manifest, _ := ParseImage("registry.example/app@sha256:" + strings.Repeat("b", 64))
	var rows []map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "platform-image-inspect.json")), &rows); err != nil {
		t.Fatal(err)
	}
	rows[0]["RepoDigests"] = []string{manifest.String()}
	local, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	container := `[{"Name":"systemd-api","Image":"748902c9f9368aa7437b05e353c23968266b0bc882ac1d74067fc1768a102ba6","Config":{"Labels":{"PODMAN_SYSTEMD_UNIT":"api.service"}},"State":{"Status":"running","Running":true}}]`
	r := &recorder{results: []localexec.Result{{}, {Stdout: container}, {Stdout: string(local)}, {Stdout: string(local)}, {Stdout: fixture(t, "manifest-inspect.json")}, {Stdout: string(local)}}}
	name, _ := ParseName("systemd-api")
	got, err := client(t, r).RunningContainerImage(context.Background(), name, "api.service", image(t), manifest)
	if err == nil {
		t.Fatal("missing local index proof accepted", got)
	}
	for _, cmd := range r.commands {
		if cmd.Mutation || cmd.Args[0] == "manifest" {
			t.Fatal("inventory requested registry or mutation", cmd.Args)
		}
	}
	if len(r.commands) != 4 {
		t.Fatal("unexpected probe count", len(r.commands))
	}
}
