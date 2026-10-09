package podman

import (
	"context"
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
			r := &recorder{results: []localexec.Result{{}, {Stdout: container}, {}, {Stdout: fixture(t, "image-inspect.json")}, {Stdout: fixture(t, "manifest-inspect.json")}, {Stdout: fixture(t, "platform-image-inspect.json")}}}
			n, _ := ParseName("systemd-api")
			got, err := client(t, r).RunningContainerImage(context.Background(), n, "api.service", image(t))
			if (err == nil) != tc.known {
				t.Fatalf("info=%+v error=%v", got, err)
			}
			if tc.known && (got.ManifestDigest != "sha256:"+strings.Repeat("b", 64) || got.ImageID != tc.image) {
				t.Fatalf("info=%+v", got)
			}
			for _, cmd := range r.commands {
				if cmd.Mutation {
					t.Fatal("inventory mutated runtime")
				}
			}
		})
	}
}
