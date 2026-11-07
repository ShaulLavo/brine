package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
	"github.com/ShaulLavo/brine/internal/ui"
)

type diagnosisPlantedLogs struct{}

func (diagnosisPlantedLogs) Read(context.Context, logs.Request) ([]logs.Line, error) {
	return []logs.Line{{Timestamp: "2026-10-09T00:00:00Z", Priority: 3, Message: "password=hunter2 Authorization: Bearer abc.def.ghi\ncopy \x1b]52;c;c2VjcmV0\a then \x1b[2J\x1b[31mred\u009b1m\rcarriage"}}, nil
}

func diagnosisTestServer() *dispatch.Server {
	s := diagnosticServer()
	r := s.Diagnose.(diagnose.Reader)
	r.Logs, r.UnitLogs = diagnosisPlantedLogs{}, diagnosisPlantedLogs{}
	s.Diagnose = r
	return s
}

func diagnosisModel(t *testing.T, deps Dependencies, flags operationFlags) tuiModel {
	t.Helper()
	m := newTUIModel(t.Context(), deps, flags, tuiEntry{})
	m.stopRead()
	m.now = func() time.Time { return fixtureReadTime }
	m.loc, m.theme = time.UTC, ui.NewTheme(true)
	m.screen = screenApps
	return m
}

func TestTUIDiagnosisJSONParity(t *testing.T) {
	for _, app := range []string{"", "demo"} {
		t.Run("scope-"+app, func(t *testing.T) { checkDiagnosisParity(t, app) })
	}
	t.Run("unknown and absent", testDiagnosisUnknown)
	t.Run("missing journals keep app logs", testDiagnosisMissingJournals)
	t.Run("decoder rejection and redaction", testDiagnosisDecoder)
}

func checkDiagnosisParity(t *testing.T, app string) {
	t.Helper()
	client := &diagnoseTransport{server: diagnosisTestServer()}
	deps := wireDependencies(t, client)
	args := []string{"diagnose"}
	if app != "" {
		args = append(args, app)
	}
	raw, err := machineJSON(t, deps, args...)
	if err != nil {
		t.Fatal(err)
	}
	want, err := diagnose.DecodeReport(raw)
	if err != nil || len(want.Findings) == 0 {
		t.Fatalf("machine report: %v %+v", err, want)
	}
	m := diagnosisModel(t, deps, lifecycleSelection)
	if app != "" {
		m.screen, m.app = screenApp, app
	}
	m, _ = press(m, "d")
	if m.diagnosis.State != loadReady || !reflect.DeepEqual(m.diagnosis.Value, want) {
		t.Fatalf("decoded facts/findings differ: %+v %v", m.diagnosis, want)
	}
	var out, diagnostics bytes.Buffer
	deps.Stdout, deps.Stderr = &out, &diagnostics
	err = Execute(deps, append(args, "--target", lifecycleSelection.target, "--config-dir", lifecycleSelection.configDir))
	if err != nil || out.String() != strings.Join(m.diagnosisText, "\n")+"\n" {
		t.Fatalf("human lines differ: %v\n%s", err, out.String())
	}
	for _, request := range client.requests {
		if request.Op != "diagnose" || string(request.Args) != `{"app":"`+app+`"}` {
			t.Fatalf("suggestion executed or scope changed: %s %s", request.Op, request.Args)
		}
	}
	all := strings.Join(m.diagnosisText, "\n")
	checkRedacted(t, all, true)
	if !strings.Contains(all, "[REDACTED]") || strings.Contains(all, "--target NAME") || !strings.Contains(all, lifecycleSelection.command("diagnose")) {
		t.Fatal(all)
	}
	m, _ = press(m, "a", "y", "enter", "l", "t")
	testDiagnosisScrolling(t, m)
}

func testDiagnosisScrolling(t *testing.T, m tuiModel) {
	t.Helper()
	before := view(m)
	m, _ = press(m, "down", "j", "up", "k")
	if view(m) != before || m.diagnosisOffset != 0 {
		t.Fatal("scroll did not return to the top")
	}
	m = m.move(10000)
	last := m.diagnosisOffset
	if last == 0 || !strings.Contains(view(m), "Next:") {
		t.Fatal("findings and suggestions cannot be reached")
	}
	m = m.move(1)
	if m.diagnosisOffset != last {
		t.Fatal("scroll exceeded the report")
	}
	m, _ = step(m, tea.WindowSizeMsg{Width: 30, Height: 8})
	checkRedacted(t, view(m), true)
}

func testDiagnosisUnknown(t *testing.T) {
	s := dispatch.NewServer("test", nil)
	s.Diagnose = diagnose.Reader{}
	m := diagnosisModel(t, wireDependencies(t, &diagnoseTransport{server: s}), lifecycleSelection)
	m, _ = press(m, "d")
	if m.diagnosis.State != loadReady || len(m.diagnosis.Value.Findings) != 0 || !strings.Contains(strings.Join(m.diagnosisText, "\n"), "inventory_unavailable") {
		t.Fatalf("unknown became healthy: %+v", m.diagnosis)
	}
	m, quit := press(m, "x")
	if !quit || !strings.Contains(m.diagnosisExit, "Unknown facts are not evidence of health.") {
		t.Fatal(m.diagnosisExit)
	}
}

type diagnosisUnavailableStore struct{ diagnoseStore }

func (diagnosisUnavailableStore) RecentOperations(context.Context, string, int) ([]ops.OperationRecord, error) {
	return nil, errors.New("PLANTED_PRIVATE_CAUSE")
}

func testDiagnosisMissingJournals(t *testing.T) {
	s := diagnosisTestServer()
	r := s.Diagnose.(diagnose.Reader)
	r.Store, r.UnitLogs = diagnosisUnavailableStore{}, nil
	s.Diagnose = r
	m := diagnosisModel(t, wireDependencies(t, &diagnoseTransport{server: s}), lifecycleSelection)
	m.screen, m.app = screenApp, "demo"
	m, _ = press(m, "d")
	a := m.diagnosis.Value.Apps[0]
	if a.Operations.Status != "unknown" || a.Operations.Reason != "probe_failed" || a.Logs.Value == nil || a.UnitLogs.Reason != "log_reader_unavailable" || a.CurrentRelease.Status != "absent" {
		t.Fatalf("missing facts were invented or app logs lost: %+v", a)
	}
	if strings.Contains(strings.Join(m.diagnosisText, "\n"), "PLANTED_PRIVATE_CAUSE") {
		t.Fatal("raw cause reached diagnosis")
	}
}

func testDiagnosisDecoder(t *testing.T) {
	for _, mutation := range []string{"findings", "scope", "logs"} {
		t.Run(mutation, func(t *testing.T) { checkDiagnosisDecoder(t, mutation) })
	}
}

func checkDiagnosisDecoder(t *testing.T, mutation string) {
	t.Helper()
	server := diagnosisTestServer()
	client := callFunc(func(ctx context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
		response, err := (&diagnoseTransport{server: server}).Call(ctx, target, req)
		if err != nil {
			return response, err
		}
		raw, err := json.Marshal(response.Data)
		if err != nil {
			return response, err
		}
		return mutatedDiagnosis(t, req.Op, raw, mutation)
	})
	m := diagnosisModel(t, wireDependencies(t, client), lifecycleSelection)
	m.screen, m.app = screenApp, "demo"
	m, _ = press(m, "d")
	if mutation == "logs" {
		checkRedacted(t, strings.Join(m.diagnosisText, "\n"), true)
		if m.diagnosis.State != loadReady || !strings.Contains(strings.Join(m.diagnosisText, "\n"), "[REDACTED]") {
			t.Fatalf("decode did not re-redact: %+v", m.diagnosis)
		}
		return
	}
	if m.diagnosis.Err == nil || m.diagnosis.Err.Code() != result.TransportInvalidResponse || m.diagnosis.known() {
		t.Fatalf("accepted corrupt diagnostic report: %+v", m.diagnosis)
	}
}

func mutatedDiagnosis(t *testing.T, op string, raw []byte, mutation string) (result.Envelope, error) {
	t.Helper()
	report, err := diagnose.DecodeReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	switch mutation {
	case "findings":
		report.Findings = []diagnose.Finding{}
	case "scope":
		report.Apps[0].Name = "another-app"
		report.AppNames = diagnose.Known([]string{"another-app"})
		report.Apps[0].Unit = diagnose.Fact[diagnose.Unit]{Status: "unknown", Reason: "probe_unavailable"}
		report.Findings = diagnose.Findings(report)
	case "logs":
		planted, _ := (diagnosisPlantedLogs{}).Read(t.Context(), logs.Request{})
		report.Apps[0].Logs, report.Apps[0].UnitLogs = diagnose.Known(planted), diagnose.Known(planted)
	}
	return reviewWire(t, op, report)
}

func TestTUIDiagnosisNavigation(t *testing.T) {
	t.Run("captured host and app", testDiagnosisCapturedScope)
	t.Run("stale and canceled replies", testDiagnosisStaleReplies)
	t.Run("failed first release and crossed pages", testDiagnosisOperationScope)
	t.Run("unbound operation refuses", testDiagnosisUnknownOperationScope)
	t.Run("exit cap and notice order", testDiagnosisNoticeOrder)
}

func testDiagnosisCapturedScope(t *testing.T) {
	h := fixtureHost()
	h.diagnose = func(ctx context.Context, app string) (diagnose.Report, error) {
		return diagnosisTestServer().Diagnose.Read(ctx, diagnose.Request{App: app})
	}
	m := start(fixtureModel(h, ""))
	m, _ = press(m, "enter", "d")
	flags := m.diagnosisFlags
	if m.diagnosisApp != "" || flags.target != "fixture" || m.screen != screenDiagnosis {
		t.Fatal("apps diagnosis did not capture host scope")
	}
	m.flags = operationFlags{target: "elsewhere", configDir: "/elsewhere"}
	m, _ = press(m, "r")
	if m.diagnosisFlags != flags || strings.Contains(m.diagnosisExitNotice(), "elsewhere") {
		t.Fatal("refresh followed browsed target")
	}
	m, _ = press(m, "q", "esc", "down", "enter", "enter", "d")
	if m.screen != screenDiagnosis || m.diagnosisApp != "fixture-api" || m.diagnosisFlags.target != "fixture-pi" {
		t.Fatalf("new target or named scope reused old report: %d %s %+v", m.screen, m.diagnosisApp, m.diagnosisFlags)
	}
	m, _ = press(m, "esc")
	if m.screen != screenApp {
		t.Fatal("diagnosis back lost its caller")
	}
}

func testDiagnosisStaleReplies(t *testing.T) {
	h := fixtureHost()
	h.diagnose = func(ctx context.Context, app string) (diagnose.Report, error) {
		return diagnosisTestServer().Diagnose.Read(ctx, diagnose.Request{App: app})
	}
	m := start(fixtureModel(h, "fixture"))
	m, _ = press(m, "d")
	before, lines := m.diagnosis, slices.Clone(m.diagnosisText)
	h.diagnose = func(context.Context, string) (diagnose.Report, error) {
		return diagnose.Report{}, result.TransportError(errors.New("PLANTED_PRIVATE_CAUSE"), result.TransportDiagnostics{})
	}
	m, _ = press(m, "r")
	if !reflect.DeepEqual(m.diagnosis.Value, before.Value) || m.diagnosis.At != before.At || !strings.Contains(view(m), "Last report is stale") || strings.Contains(view(m), "PLANTED_PRIVATE_CAUSE") {
		t.Fatal("failed refresh lost or mislabeled last report")
	}
	started, canceled, done := make(chan string, 1), make(chan string, 1), make(chan tea.Msg, 1)
	h.diagnose = func(ctx context.Context, _ string) (diagnose.Report, error) {
		started <- "diagnosis"
		<-ctx.Done()
		canceled <- "diagnosis"
		return diagnose.Report{}, ctx.Err()
	}
	m, cmd := model(m.refresh())
	t.Cleanup(m.stopRead)
	go func() { done <- cmd() }()
	receive(t, started, "diagnosis")
	oldToken := m.token
	m, _ = press(m, "esc")
	receive(t, canceled, "diagnosis")
	m, _ = step(m, <-done)
	m, _ = step(m, diagnosisMsg{token: oldToken, report: diagnose.Report{}})
	if m.screen != screenApps || !reflect.DeepEqual(m.diagnosis.Value, before.Value) || !slices.Equal(m.diagnosisText, lines) {
		t.Fatal("late diagnosis overwrote another screen")
	}
	m, cmd = model(m.openDiagnosis())
	m, _ = step(m, diagnosisMsg{token: oldToken, err: errors.New("PLANTED_PRIVATE_CAUSE")})
	if m.diagnosis.State != loadLoading || cmd == nil || !strings.Contains(view(m), "Last report is stale") {
		t.Fatal("stale token ended a newer refresh")
	}
	m.stopRead()
}

func testDiagnosisUnknownOperationScope(t *testing.T) {
	for _, bound := range []bool{false, true} {
		m := fixtureModel(fixtureHost(), "fixture")
		m.stopRead()
		m.screen = screenOperation
		m.observe = &tuiObservation{Bound: bound, Flags: lifecycleSelection, Operation: ops.Operation{ID: "op-direct"}}
		after, cmd := model(m.openDiagnosis())
		if cmd != nil || after.screen != screenOperation || after.diagnosisFlags.target != "" {
			t.Fatal("unknown app silently diagnosed the host")
		}
	}
}

func testDiagnosisOperationScope(t *testing.T) {
	for _, crossed := range []string{"app", "plan", "kind"} {
		t.Run(crossed, func(t *testing.T) { checkDiagnosisOperationScope(t, crossed) })
	}
}

func checkDiagnosisOperationScope(t *testing.T, crossed string) {
	t.Helper()
	calls := 0
	diagnostic := &diagnoseTransport{server: diagnosisTestServer()}
	client := callFunc(func(ctx context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
		if req.Op == "diagnose" {
			return diagnostic.Call(ctx, target, req)
		}
		calls++
		return diagnosisOperationPage(t, req, calls, crossed)
	})
	m := directOperationModel(t, client)
	original := *m.observe
	m, _ = press(m, "r")
	if m.observe.Err == nil || m.observe.Cursor != original.Cursor || m.observe.Operation != original.Operation {
		t.Fatal("crossed page replaced the accepted identity")
	}
	m.flags = operationFlags{target: "elsewhere", configDir: "/elsewhere"}
	m, _ = press(m, "d")
	if m.diagnosisFlags != lifecycleSelection || m.diagnosisApp != "demo" || m.observe.Cursor != original.Cursor || m.diagnosis.State != loadReady {
		t.Fatalf("diagnosis lost observation scope: %+v", m.diagnosis)
	}
	a := m.diagnosis.Value.Apps[0]
	if a.CurrentRelease.Status != "absent" || a.Operations.Value == nil || (*a.Operations.Value)[0].State != ops.Failed {
		t.Fatal("failed first release required a release row")
	}
	m, _ = press(m, "q")
	if m.screen != screenOperation || m.observe.Cursor != original.Cursor+1 || m.observe.Operation.App != "demo" {
		t.Fatal("back reset the original cursor")
	}
	m, _ = press(m, "d", "x")
	var out bytes.Buffer
	if err := writeTUIExitNotice(m, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "elsewhere") || !strings.Contains(out.String(), lifecycleSelection.command("diagnose", "demo")) || strings.Index(out.String(), "Observe with:") > strings.Index(out.String(), "Diagnosis for") {
		t.Fatal(out.String())
	}
}

func diagnosisOperationPage(t *testing.T, req dispatch.Request, calls int, crossed string) (result.Envelope, error) {
	t.Helper()
	if req.Op != "operation" {
		t.Fatalf("suggestion executed: %s", req.Op)
	}
	var args dispatch.OperationArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		t.Fatal(err)
	}
	page := operationFixture(ops.Preflight, args.AfterCursor, 1)
	page.Operation.ID, page.Operation.App = "op-direct", "demo"
	if calls == 2 {
		crossDirectPage(&page, crossed)
	}
	return reviewWire(t, req.Op, page)
}

func testDiagnosisNoticeOrder(t *testing.T) {
	r, err := diagnosisTestServer().Diagnose.Read(t.Context(), diagnose.Request{App: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	r.Host.Linger, r.Apps[0].Health, r.Apps[0].Drift = diagnose.Known(false), diagnose.Known(false), diagnose.Known([]string{"units"})
	r.Findings = diagnose.Findings(r)
	if len(r.Findings) <= diagnosisSummaryLimit {
		t.Fatal("fixture does not exercise summary cap")
	}
	m := fixtureModel(fixtureHost(), "fixture")
	m.stopRead()
	m.screen, m.diagnosisFlags, m.diagnosisApp = screenDiagnosis, lifecycleSelection, "demo"
	m.diagnosis.finish(r, nil, fixtureReadTime)
	m.diagnosis.State = loadFailed
	m.issued = &issuedApply{Flags: lifecycleSelection, Review: tuiReview{PlanID: lifecyclePlanID, Key: "original-key"}}
	m, quit := press(m, "x")
	if !quit {
		t.Fatal("explicit x did not quit")
	}
	var out bytes.Buffer
	if err := writeTUIExitNotice(m, &out); err != nil {
		t.Fatal(err)
	}
	want := m.issuedExitNotice() + "\n" + m.diagnosisExit + "\n"
	if out.String() != want || !strings.Contains(out.String(), "Last report is stale.") || !strings.Contains(out.String(), fmt.Sprintf("%d more findings omitted.", len(r.Findings)-diagnosisSummaryLimit)) || !strings.Contains(out.String(), lifecycleSelection.command("diagnose", "demo")) {
		t.Fatal(out.String())
	}
	for _, finding := range r.Findings[diagnosisSummaryLimit:] {
		if strings.Contains(m.diagnosisExit, finding.Code) {
			t.Fatal("summary exceeded six finding codes")
		}
	}
	m.diagnosisExit = ""
	m, _ = press(m, "q")
	if m.diagnosisExit != "" {
		t.Fatal("ordinary quit requested a diagnostic summary")
	}
}

var _ tuiHost = (*fakeTUIHost)(nil)
