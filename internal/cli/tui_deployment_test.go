package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/transport"
)

func deploymentSource(t *testing.T, suffix string) string {
	t.Helper()
	raw, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "brine.toml")
	if err := os.WriteFile(path, append(raw, suffix...), 0600); err != nil { // #nosec G703 -- Fixed filename in the test-owned temporary directory.
		t.Fatal(err)
	}
	return path
}

type deploymentWireHost struct {
	t        *testing.T
	kind     plan.Kind
	failure  string
	requests []string
	plans    []dispatch.PlanArgs
	applies  []dispatch.ApplyArgs
}

func (h *deploymentWireHost) Call(_ context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
	h.t.Helper()
	h.requests = append(h.requests, target.Name+" "+req.Op)
	switch req.Op {
	case "plan":
		var args dispatch.PlanArgs
		if err := json.Unmarshal(req.Args, &args); err != nil {
			h.t.Fatal(err)
		}
		h.plans = append(h.plans, args)
		p := dispatch.Planned{PlanID: lifecyclePlanID, Kind: h.kind, SchemaArtifacts: []data.SchemaArtifact{}, Conflicts: []plan.Diagnostic{}}
		if h.kind == plan.Conflict {
			p.Conflicts = []plan.Diagnostic{{Code: plan.ArtifactDrift, Field: "unit"}}
		}
		return reviewWire(h.t, req.Op, p)
	case "apply":
		var args dispatch.ApplyArgs
		if err := json.Unmarshal(req.Args, &args); err != nil {
			h.t.Fatal(err)
		}
		h.applies = append(h.applies, args)
		if len(h.applies) == 1 && h.failure == "lost" {
			return result.Envelope{}, result.New(result.TransportFailure, nil)
		}
		if len(h.applies) == 1 && h.failure == "malformed" {
			return dispatch.DecodeResponse([]byte(`{"schema_version":1,"command":"brine host apply","ok":true,"data":{"status":"accepted","operation_id":"bad id"},"error":null}`), req.Op)
		}
		return reviewWire(h.t, req.Op, jobs.Accepted{Status: "accepted", OperationID: "op-release"})
	case "operation":
		status := operationFixture(ops.Starting, 0, 1)
		status.Operation.ID, status.Operation.App, status.Operation.PlanID = "op-release", "hello", lifecyclePlanID
		return reviewWire(h.t, req.Op, status)
	default:
		h.t.Fatalf("unexpected request %s", req.Op)
		return result.Envelope{}, result.New(result.InternalError, nil)
	}
}

func deploymentModel(t *testing.T, h *deploymentWireHost, path string) tuiModel {
	t.Helper()
	m := start(newTUIModel(t.Context(), wireDependencies(t, h), lifecycleSelection, tuiEntry{Spec: path}))
	m.now = func() time.Time { return fixtureReadTime }
	m.newKey = func() (string, error) { return "release-original-key", nil }
	m.after = func(time.Duration, tea.Msg) tea.Cmd { return nil }
	return m
}

func TestTUIDeploymentEntry(t *testing.T) {
	for _, kind := range []plan.Kind{plan.Create, plan.Update, plan.Conflict, plan.NoOp} {
		t.Run(string(kind), func(t *testing.T) { checkDeploymentEntry(t, kind) })
	}
	t.Run("picker", testDeploymentPicker)
	t.Run("invalid flags", testDeploymentEntryFlags)
	t.Run("preflight", testDeploymentPreflight)
	t.Run("runner", testDeploymentEntryRunner)
}

func checkDeploymentEntry(t *testing.T, kind plan.Kind) {
	t.Helper()
	h := &deploymentWireHost{t: t, kind: kind}
	m := deploymentModel(t, h, deploymentSource(t, ""))
	if m.screen != screenReview || m.review.State != loadReady || m.review.Value.App != "hello" || len(h.requests) != 1 || h.requests[0] != "fixture plan" {
		t.Fatalf("entry: %+v %v", m.review, h.requests)
	}
	if m.canApply() != (kind == plan.Create || kind == plan.Update) {
		t.Fatalf("apply enabled for %s", kind)
	}
	m, _ = press(m, "enter")
	m, _ = press(m, "r")
	if len(h.plans) != 2 {
		t.Fatal("refresh did not replan")
	}
	m, _ = model(m.reload())
	if len(h.requests) != 2 {
		t.Fatal("review navigation read unrelated apps")
	}
}

func testDeploymentPicker(t *testing.T) {
	h := &deploymentWireHost{t: t, kind: plan.Create}
	deps := wireDependencies(t, h)
	m := newTUIModel(t.Context(), deps, operationFlags{configDir: lifecycleSelection.configDir}, tuiEntry{Spec: deploymentSource(t, "")})
	m.stopRead()
	m.listTargets = func() ([]string, error) { return []string{"fixture"}, nil }
	m, m.initial = m.loadTargets()
	m = start(m)
	if m.screen != screenTargets || len(h.requests) != 0 {
		t.Fatal("sole target selected automatically")
	}
	m, _ = press(m, "enter")
	if m.screen != screenReview || m.review.State != loadReady {
		t.Fatalf("picked target: %+v", m.review)
	}
	m, _ = press(m, "esc", "enter")
	if len(h.plans) != 2 {
		t.Fatal("back did not return to entry")
	}
}

func testDeploymentEntryFlags(t *testing.T) {
	for _, args := range [][]string{{"--spec", ""}, {"--operation", ""}, {"--operation", "bad id"}, {"--spec", "a", "--operation", "op-a"}, {"extra"}, {"--spec", "file\x1b[31m"}} {
		var out, diagnostics bytes.Buffer
		deps := testDependencies(t, &out, &diagnostics)
		if err := Execute(deps, append([]string{"tui"}, args...)); result.Classify(err).Code() != result.InvalidUsage {
			t.Fatalf("%q: %v", args, err)
		}
	}
}

func testDeploymentPreflight(t *testing.T) {
	for _, kind := range []string{"database", "managed", "udp", "missing", "malformed", "oversized", "directory"} {
		t.Run(kind, func(t *testing.T) { checkDeploymentPreflight(t, kind) })
	}
}

func checkDeploymentPreflight(t *testing.T, kind string) {
	t.Helper()
	path := deploymentSource(t, "")
	switch kind {
	case "database":
		path = deploymentSource(t, "\n[runtime]\nuid=10001\ngid=10001\n[[databases]]\nname=\"main\"\npersistent_root=\"/srv/brine-data\"\nmount_path=\"/data\"\nfilename=\"app.db\"\nbackup_destination=\"primary\"\nschema=\"missing.sql\"\n")
	case "managed":
		raw, err := os.ReadFile("../spec/testdata/managed.toml")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil { // #nosec G703 -- Synthetic fixture bytes in a test-owned local file.
			t.Fatal(err)
		}
	case "udp":
		path = deploymentSource(t, "\n[udp]\ncontainer_port=3001\nexposure=\"public\"\n")
	case "missing":
		path += ".absent"
	case "malformed":
		if err := os.WriteFile(path, []byte("name = [PLANTED_PRIVATE_SPEC"), 0600); err != nil {
			t.Fatal(err)
		}
	case "oversized":
		if err := os.WriteFile(path, []byte(strings.Repeat("x", dispatch.RequestLimit/2+1)), 0600); err != nil {
			t.Fatal(err)
		}
	case "directory":
		path = filepath.Dir(path)
	}
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	withTestTerminal(t, &deps)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
		t.Fatal("preflight read target pin")
		return transport.Target{}, nil
	}
	deps.RunTUI = func(_ context.Context, model tea.Model, _ io.Reader, _ io.Writer) (tea.Model, error) {
		m := start(model.(tuiModel))
		if m.review.Err == nil || m.review.Err.Code() != result.InvalidUsage || strings.Contains(view(m), "PLANTED_PRIVATE_SPEC") {
			t.Fatalf("preflight: %+v", m.review)
		}
		return m, nil
	}
	if err := Execute(deps, []string{"tui", "--spec", path, "--target", "fixture"}); err != nil {
		t.Fatal(err)
	}
	if kind == "database" || kind == "managed" || kind == "udp" {
		_, err := readTUIDeploymentInput(t.Context(), path)
		if result.Classify(err).Code() != result.InvalidUsage {
			t.Fatalf("scope resolved missing artifact: %v", err)
		}
	}
}

func TestTUIDeploymentReviewRecovery(t *testing.T) {
	for _, failure := range []string{"lost", "malformed"} {
		t.Run(failure, func(t *testing.T) { checkDeploymentRecovery(t, failure) })
	}
	t.Run("summary ownership", testDeploymentSummaryOwnership)
	t.Run("late acceptance", testDeploymentLateAcceptance)
	t.Run("confirmation visibility", testDeploymentConfirmationVisibility)
}

func checkDeploymentRecovery(t *testing.T, failure string) {
	t.Helper()
	h := &deploymentWireHost{t: t, kind: plan.Update, failure: failure}
	path := deploymentSource(t, "\n[environment]\nZ_KEY=\"PLANTED_ENV_VALUE\"\nA_KEY=\"PRIVATE_VALUE\"\n")
	m := deploymentModel(t, h, path)
	summary := *m.review.Value.DeploymentSummary
	if !slices.Equal(summary.EnvironmentKeys, []string{"A_KEY", "Z_KEY"}) || m.review.Value.Deployment.Diff != nil {
		t.Fatalf("summary not from exact unchanged input: %+v", summary)
	}
	allLines := strings.Join(m.reviewLines(), "\n")
	if !strings.Contains(strings.ReplaceAll(allLines, "\n", ""), summary.Image) || !strings.Contains(allLines, "hello.example.com") || !strings.Contains(allLines, "Health /") || strings.Contains(allLines, "PLANTED_ENV_VALUE") || strings.Contains(allLines, "PRIVATE_VALUE") {
		t.Fatal(allLines)
	}
	m, apply := issueForTest(m)
	if apply == nil {
		t.Fatal("visible confirmation did not issue")
	}
	m, _ = updateTUI(m, apply())
	original := m.issued
	if original == nil || original.Accepted != nil || original.Err == nil {
		t.Fatal("lost acceptance not retained")
	}
	if err := os.WriteFile(path, []byte("broken edited source"), 0600); err != nil {
		t.Fatal(err)
	}
	m, _ = press(m, "r")
	if len(h.plans) != 1 {
		t.Fatal("issued review reread changed file")
	}
	m.flags = operationFlags{target: "elsewhere", configDir: "/elsewhere"}
	m.screen = screenApps
	m, _ = press(m, "u")
	if !reflect.DeepEqual(*m.review.Value.DeploymentSummary, summary) || m.reviewFlags != lifecycleSelection {
		t.Fatal("recovery changed summary or selection")
	}
	m, cmd := issueForTest(m)
	m, _ = updateTUI(m, cmd())
	if len(h.applies) != 2 || h.applies[0] != h.applies[1] || len(h.plans) != 1 || m.issued != original {
		t.Fatalf("not same-key recovery: %v %v", h.applies, h.requests)
	}
	if m.observe == nil || m.observe.Flags != lifecycleSelection {
		t.Fatal("acceptance observed another target")
	}
}

func testDeploymentSummaryOwnership(t *testing.T) {
	app := spec.App{Domains: []spec.Domain{"hello.example.com"}, Environment: map[string]string{"Z_KEY": "private", "A_KEY": "secret"}}
	summary := summarizeDeployment(app)
	app.Domains[0] = "changed.example.com"
	app.Environment["B_KEY"] = "changed"
	if !slices.Equal(summary.Domains, []string{"hello.example.com"}) || !slices.Equal(summary.EnvironmentKeys, []string{"A_KEY", "Z_KEY"}) {
		t.Fatalf("borrowed summary slices: %+v", summary)
	}
}

func testDeploymentLateAcceptance(t *testing.T) {
	h := &deploymentWireHost{t: t, kind: plan.Create}
	m := deploymentModel(t, h, deploymentSource(t, ""))
	m, apply := issueForTest(m)
	original := m.issued
	m.screen, m.flags = screenTargets, operationFlags{configDir: "/elsewhere"}
	m, _ = updateTUI(m, apply())
	if m.screen != screenTargets || m.issued != original || original.Accepted == nil {
		t.Fatal("late acceptance lost after navigation")
	}
	m, observe := model(m.openIssued())
	if observe == nil || m.observe.Flags != lifecycleSelection {
		t.Fatal("late acceptance did not reopen original observation")
	}
}

func testDeploymentConfirmationVisibility(t *testing.T) {
	h := &deploymentWireHost{t: t, kind: plan.Create}
	m := deploymentModel(t, h, deploymentSource(t, ""))
	m, _ = press(m, "y")
	if m.issued != nil {
		t.Fatal("y without a issued apply")
	}
	m.width, m.height = 1, 1
	m, cmd := issueForTest(m)
	if cmd != nil || m.issued != nil {
		t.Fatal("invisible confirmation issued")
	}
}

func testDeploymentEntryRunner(t *testing.T) {
	for _, failure := range []string{"", "lost", "malformed"} {
		t.Run(failure, func(t *testing.T) { runDeploymentEntry(t, failure) })
	}
}

func runDeploymentEntry(t *testing.T, failure string) {
	t.Helper()
	input, keys := pipe(t)
	output := &lockedBuffer{}
	var diagnostics bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started, canceled := make(chan string, 1), make(chan string, 1)
	h := &deploymentWireHost{t: t, kind: plan.Create, failure: failure}
	client := callFunc(func(ctx context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
		if req.Op != "operation" {
			return h.Call(ctx, target, req)
		}
		started <- "operation"
		<-ctx.Done()
		canceled <- "operation"
		return result.Envelope{}, ctx.Err()
	})
	deps := wireDependencies(t, client)
	deps.Context, deps.Stdin, deps.Stdout, deps.Stderr = ctx, input, output, &diagnostics
	withTestTerminal(t, &deps)
	deps.RunTUI = func(ctx context.Context, model tea.Model, _ io.Reader, _ io.Writer) (tea.Model, error) {
		m := model.(tuiModel)
		m.newKey = func() (string, error) { return "release-runner-key", nil }
		return RunTUI(ctx, m, input, output)
	}
	path := deploymentSource(t, "")
	done := startEntryCommand(t, deps, []string{"tui", "--spec", path, "--target", "fixture", "--config-dir", lifecycleSelection.configDir}, cancel)
	waitForOutput(t, output, "a apply")
	writeKeys(t, keys, "a")
	waitForOutput(t, output, "y apply")
	writeKeys(t, keys, "y")
	if failure != "" {
		waitForOutput(t, output, "Acceptance is unknown")
		if err := os.WriteFile(path, []byte("changed file must not be planned"), 0600); err != nil {
			t.Fatal(err)
		}
		writeKeys(t, keys, "ruay")
	}
	receive(t, started, "operation")
	writeKeys(t, keys, "\x03")
	if err := awaitEntryCommand(t, done); err != nil {
		t.Fatal(err)
	}
	receive(t, canceled, "operation")
	expected := []string{"fixture plan", "fixture apply"}
	if failure != "" {
		expected = append(expected, "fixture apply")
	}
	if !slices.Equal(h.requests, expected) || len(h.applies) != len(expected)-1 || h.applies[0].IdempotencyKey != "release-runner-key" {
		t.Fatalf("runner used another path: %v %+v", h.requests, h.applies)
	}
	if len(h.applies) == 2 && h.applies[0] != h.applies[1] {
		t.Fatal("runner recovery changed plan or key")
	}
	if !strings.Contains(diagnostics.String(), lifecycleSelection.command("status", "--operation", "op-release", "--after-cursor", "0")) {
		t.Fatal(diagnostics.String())
	}
}
