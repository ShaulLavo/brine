package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestDeploymentInputCommandParity(t *testing.T) {
	t.Run("stateless request and raw resume identity", func(t *testing.T) { testCLIReleaseInput(t, true) })
	t.Run("schema request and raw resume identity", func(t *testing.T) { testCLIReleaseInput(t, false) })
	t.Run("managed module resolution", testCLIManagedInput)
	t.Run("invalid input redaction", testCLIInputRefusal)
}

func expectedDeploymentRequest(t *testing.T, path string) dispatch.PlanArgs {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- Test-owned path returned by a local fixture.
	if err != nil {
		t.Fatal(err)
	}
	app, err := parseClientSpec(t.Context(), path, raw, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	expected, err := dispatch.NewPlanArgs(string(raw), app)
	if err != nil {
		t.Fatal(err)
	}
	return expected
}

func testCLIReleaseInput(t *testing.T, stateless bool) {
	t.Helper()
	host, deps, args, out, diagnostics := deploymentFixture(t)
	host.stateless = stateless
	path, configDir := args[1], args[5]
	if stateless {
		raw, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil { // #nosec G703 -- Existing test-owned deployment fixture file.
			t.Fatal(err)
		}
	}
	expected := expectedDeploymentRequest(t, path)
	if stateless {
		input, err := readTUIDeploymentInput(t.Context(), path)
		if err != nil || !reflect.DeepEqual(input.Request, expected) {
			t.Fatalf("TUI request differs: %+v %v", input, err)
		}
	} else if len(expected.SchemaArtifacts) != 1 {
		t.Fatal("SQL schema did not resolve")
	}
	captured := 0
	deps.OperationClient = callFunc(func(ctx context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
		if req.Op == "plan" || req.Op == "deploy_state" {
			checkDeploymentRequest(t, req, expected)
			captured++
		}
		return host.Call(ctx, target, req)
	})
	if err := Execute(deps, []string{"plan", path, "--target", "fixture", "--config-dir", configDir, "--json"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Execute(deps, args); err != nil {
		t.Fatal(err, diagnostics.String())
	}
	if captured < 2 {
		t.Fatal("real plan/deploy handlers did not use the expected request")
	}
	first := deploymentResumePath(t, configDir, expected)
	if _, err := os.Stat(filepath.Join(first, "journal.json")); err != nil {
		t.Fatal("raw-input resume identity changed", err)
	}
	out.Reset()
	if err := Execute(deps, args); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(configDir, "deploys"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("exact resume made a new identity: %v %v", entries, err)
	}
	expected.Spec += "\n# same intent, distinct original bytes\n"
	if err := os.WriteFile(path, []byte(expected.Spec), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Execute(deps, args); err != nil {
		t.Fatal(err)
	}
	second := deploymentResumePath(t, configDir, expected)
	if second == first {
		t.Fatal("distinct raw input reused resume identity")
	}
	if _, err := os.Stat(filepath.Join(second, "journal.json")); err != nil {
		t.Fatal("changed source did not create exact raw-input identity", err)
	}
}

func checkDeploymentRequest(t *testing.T, req dispatch.Request, expected dispatch.PlanArgs) {
	t.Helper()
	var actual dispatch.PlanArgs
	if err := json.Unmarshal(req.Args, &actual); err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("%s request changed: %+v %v", req.Op, actual, err)
	}
}

func deploymentResumePath(t *testing.T, configDir string, request dispatch.PlanArgs) string {
	t.Helper()
	identity, err := json.Marshal(struct {
		Target    transport.Target
		Spec      string
		Artifacts []data.SchemaArtifact
	}{transport.Target{Name: "fixture"}, request.Spec, request.SchemaArtifacts})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(identity)
	return filepath.Join(configDir, "deploys", hex.EncodeToString(sum[:]))
}

func testCLIManagedInput(t *testing.T) {
	path := managedValidateSpec(t, false)
	expected := expectedDeploymentRequest(t, path)
	if expected.ManagedModule == nil {
		t.Fatal("managed module not resolved")
	}
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	calls := 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
		calls++
		if req.Op != "plan" {
			t.Fatalf("managed refusal attempted %s", req.Op)
		}
		checkDeploymentRequest(t, req, expected)
		return result.Success("brine host plan", dispatch.Planned{PlanID: lifecyclePlanID, Kind: plan.Conflict, Conflicts: []plan.Diagnostic{{Code: plan.ManagedEngineUnsupported, Field: "databases.spacetimedb"}}}), nil
	})
	if err := Execute(deps, []string{"plan", path, "--target", "fixture", "--json"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Execute(deps, []string{"deploy", path, "--target", "fixture", "--yes", "--json"}); result.Classify(err).Code() != result.Conflict {
		t.Fatalf("managed command behavior changed: %v", err)
	}
	if calls != 2 {
		t.Fatal("managed command paths not executed")
	}
}

func testCLIInputRefusal(t *testing.T) {
	for _, command := range []string{"plan", "deploy"} {
		t.Run(command, func(t *testing.T) { checkCLIInputRefusal(t, command) })
	}
}

func checkCLIInputRefusal(t *testing.T, command string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	path := deploymentSource(t, "")
	if err := os.WriteFile(path, []byte("name = [PLANTED_PRIVATE_SPEC"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Execute(deps, []string{command, path, "--target", "fixture", "--json"}); result.Classify(err).Code() != result.InvalidUsage {
		t.Fatal(err)
	}
	if strings.Contains(out.String()+diagnostics.String(), "PLANTED_PRIVATE_SPEC") {
		t.Fatal("raw spec leaked")
	}
}
