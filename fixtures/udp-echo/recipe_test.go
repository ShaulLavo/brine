package udpecho

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestImageRecipeMatchesRepositoryPins(t *testing.T) {
	var pins struct {
		GoVersion              string            `json:"go_version"`
		GoBuilder              string            `json:"go_builder"`
		BuilderPlatform        map[string]string `json:"builder_platform_manifests"`
		RuntimeBase            string            `json:"runtime_base"`
		RuntimeUser            string            `json:"runtime_user"`
		ThirdPartyDependencies []string          `json:"third_party_dependencies"`
	}
	if err := json.Unmarshal(readFile(t, "fixtures/udp-echo/pins.json"), &pins); err != nil {
		t.Fatal(err)
	}
	mise := string(readFile(t, "mise.toml"))
	if !strings.Contains(mise, `go = "`+pins.GoVersion+`"`) {
		t.Fatal("fixture toolchain differs from repository pin")
	}
	dockerfile := string(readFile(t, "fixtures/udp-echo/Dockerfile"))
	if !strings.Contains(dockerfile, "golang:"+pins.GoVersion+"@sha256:") || !strings.Contains(dockerfile, pins.GoBuilder+" AS build") {
		t.Fatal("image builder differs from verified pin")
	}
	if len(pins.BuilderPlatform) != 2 || pins.BuilderPlatform["linux/amd64"] == "" || pins.BuilderPlatform["linux/arm64"] == "" {
		t.Fatal("both platform pins are required")
	}
	if pins.RuntimeBase != "scratch" || pins.RuntimeUser != "65532:65532" || len(pins.ThirdPartyDependencies) != 0 {
		t.Fatal("fixture runtime or dependency contract changed")
	}
	if strings.Count(dockerfile, "USER "+pins.RuntimeUser) != 2 || strings.Count(dockerfile, "--chmod=0555") != 2 {
		t.Fatal("both image targets must be non-root and executable")
	}
	if strings.Contains(dockerfile, "go mod download") || strings.Contains(dockerfile, "--push") || strings.Contains(dockerfile, "# syntax=") {
		t.Fatal("recipe adds unnecessary downloads or publication")
	}
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	root, err := os.OpenRoot("../..")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
