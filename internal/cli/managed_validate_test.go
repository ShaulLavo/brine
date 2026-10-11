package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func managedValidateSpec(t *testing.T, frontend bool) string {
	t.Helper()
	raw, err := os.ReadFile("../spec/testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	if frontend {
		raw = []byte("image=\"registry.example.com:5000/team/web@sha256:" + strings.Repeat("a", 64) + "\"\ncontainer_port=8080\nresources={memory_mb=128,pids_limit=128}\n" + string(raw))
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "module"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"brine.toml": raw, "module/game.wasm": {0, 'a', 's', 'm', 1, 0, 0, 0}, "module/schema-v10.json": []byte(`{"sections":[{"Typespace":{"types":[]}}]}`)}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "brine.toml")
}

func checkManagedValidate(t *testing.T, frontend bool) {
	t.Helper()
	out, diagnostics, exit := offlineExecute(t, "validate", managedValidateSpec(t, frontend), "--policy", "../policy/testdata/managed-operator.toml")
	if exit != 0 || diagnostics != "" {
		t.Fatalf("validate exit %d, output %q, diagnostics %q", exit, out, diagnostics)
	}
	if !strings.Contains(out, "Managed engine: SpacetimeDB 2.11.0\nEngine image: registry.example.com:5000/team/engine@sha256:") || !strings.Contains(out, "Database: game\n") {
		t.Fatal("managed engine details missing")
	}
	if strings.Contains(out, "Image: \n") || strings.Contains(out, "Container port: 0\n") || strings.Contains(out, "\x1b") {
		t.Fatal("missing frontend became a bogus container or unsafe output")
	}
	if frontend && !strings.Contains(out, "Frontend image: registry.example.com:5000/team/web@sha256:") {
		t.Fatal("frontend image missing")
	}
	if frontend && !strings.Contains(out, "Frontend container port: 8080\n") {
		t.Fatal("frontend port missing")
	}
	if !frontend && !strings.Contains(out, "Frontend: none\n") {
		t.Fatal("engine-only presentation did not state frontend absence")
	}
}

func TestManagedValidateHumanPresentation(t *testing.T) {
	t.Run("engine only", func(t *testing.T) { checkManagedValidate(t, false) })
	t.Run("frontend", func(t *testing.T) { checkManagedValidate(t, true) })
}
