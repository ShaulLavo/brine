package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/planfile"
)

func managedValidateSpec(t *testing.T, frontend bool) string {
	t.Helper()
	raw, err := os.ReadFile("../spec/testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	if frontend {
		raw = []byte("image=\"registry.example.com:5000/team/web@sha256:" + strings.Repeat("a", 64) + "\"\ncontainer_port=8080\n" + string(raw))
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
	if !strings.Contains(out, "Engine resources: 128 MiB memory, 128 PIDs\n") || frontend && !strings.Contains(out, "Frontend resources: 384 MiB memory, 384 PIDs\n") {
		t.Fatal("derived engine/frontend budgets missing")
	}
	if !frontend && !strings.Contains(out, "Frontend: none\n") {
		t.Fatal("engine-only presentation did not state frontend absence")
	}
}

func TestManagedValidateHumanPresentation(t *testing.T) {
	t.Run("engine only", func(t *testing.T) { checkManagedValidate(t, false) })
	t.Run("frontend", func(t *testing.T) { checkManagedValidate(t, true) })
}

func checkManagedOfflinePlan(t *testing.T, frontend bool) {
	t.Helper()
	args := []string{"plan", managedValidateSpec(t, frontend), "--offline", "--snapshot", offlineExamples + "fresh-host.json", "--policy", "../policy/testdata/managed-operator.toml"}
	out, diagnostics, exit := offlineExecute(t, args...)
	if exit != 0 || diagnostics != "" || !strings.Contains(out, "managed_engine_not_admitted") {
		t.Fatalf("managed offline plan exit %d, output %q, diagnostics %q", exit, out, diagnostics)
	}
	if !strings.Contains(out, "Engine resources: 128 MiB memory, 128 PIDs") || frontend && !strings.Contains(out, "Frontend resources: 384 MiB memory, 384 PIDs") {
		t.Fatal("managed plan omitted resource budgets")
	}
	directory := t.TempDir()
	out, diagnostics, exit = offlineExecute(t, append(args, "--out", directory, "--json")...)
	if exit != 0 || diagnostics != "" {
		t.Fatalf("machine plan exit %d, output %q, diagnostics %q", exit, out, diagnostics)
	}
	var envelope struct {
		Data plan.Plan `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatal(err)
	}
	p := envelope.Data.ManagedPreview
	if p == nil || p.EngineHostPort != 20000 || p.EngineResources.MemoryMB != 128 || !frontend && (p.AppHostPort != 0 || p.AppResources != nil) {
		t.Fatal("machine preview omitted engine identity/budget or created a dummy frontend")
	}
	if frontend && (p.AppHostPort != 20001 || p.AppResources == nil || p.AppResources.MemoryMB != 384) {
		t.Fatal("machine preview omitted frontend port/budget")
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 1 {
		t.Fatal("offline preview did not persist exactly one plan", err)
	}
	document, err := planfile.Read(filepath.Join(directory, files[0].Name()))
	if err != nil || document.Applyable() || document.Plan().ManagedPreview == nil {
		t.Fatal("persisted managed preview did not round trip without apply authority", err)
	}
}

func TestManagedOfflinePlanSupportsEngineOnlyAndFrontend(t *testing.T) {
	t.Run("engine only", func(t *testing.T) { checkManagedOfflinePlan(t, false) })
	t.Run("frontend", func(t *testing.T) { checkManagedOfflinePlan(t, true) })
}
