package brinedrill

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/fixtures/udp-echo/client"
	"github.com/ShaulLavo/brine/fixtures/udp-echo/protocol"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/udpready"
)

// These builders exercise the JSON fields used by a parent-authored manifest.
func drillStep(t *testing.T, value map[string]any) Step {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var step Step
	if err := json.Unmarshal(raw, &step); err != nil {
		t.Fatal(err)
	}
	return step
}

func stepRelease(t *testing.T, id, release, image, outcome string) Step {
	t.Helper()
	return drillStep(t, map[string]any{"id": id, "action": "apply", "app": "udp-a", "plan_id": "sha256:" + strings.Repeat(id[:1], 64), "expected_outcome": outcome, "expectation": map[string]string{"release": release, "image_digest": image}})
}

func stepObservation(t *testing.T, manifest Manifest, id, source, release, image, operation string) []Step {
	t.Helper()
	proof := map[string]any{"schema": ReceiptSchema, "run_id": manifest.RunID, "target": manifest.Target.Name, "kind": "ready", "app": "udp-a", "incarnation": "sha256:" + strings.Repeat("d", 64), "release": release, "release_id": operation, "image_digest": image, "endpoint": manifest.Apps[0].Endpoint, "http_health": "healthy", "network_mode": "pasta", "mapping": MappingProof{Present: true, BindAddress: manifest.Apps[0].Endpoint.BindAddress, HostPort: 4433, ContainerPort: 4433}, "socket": SocketProof{Address: manifest.Apps[0].Endpoint.BindAddress, Port: 4433, App: "udp-a", Process: "pasta", Unit: "udp-a.service", Inode: 42, Source: "ss -H -lunpe", Ownership: "affirmed"}, "inode_gate": "confirmed", "foreign_overlap": "none", "route_unchanged": true}
	path := filepath.Join(t.TempDir(), id+".json")
	writeJSON(t, path, proof)
	return []Step{
		drillStep(t, map[string]any{"id": id, "action": "status", "app": "udp-a", "release_step": source, "expected_outcome": "ready", "proof": path, "proof_sha256": mustFileHash(t, path)}),
		drillStep(t, map[string]any{"id": id + "-probe", "action": "client-probe", "app": "udp-a", "release_step": source, "expected_outcome": "ok", "nonce": id + "-nonce", "vantage": "external-client"}),
	}
}

func releaseSequence(t *testing.T) (Manifest, FileLedger) {
	t.Helper()
	m, l, cleanup := testManifest(t)
	t.Cleanup(cleanup)
	raw, err := json.Marshal(m.Apps[0])
	if err != nil {
		t.Fatal(err)
	}
	var app map[string]any
	if err := json.Unmarshal(raw, &app); err != nil {
		t.Fatal(err)
	}
	app["incarnation"] = "sha256:" + strings.Repeat("d", 64)
	raw, err = json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &m.Apps[0]); err != nil {
		t.Fatal(err)
	}
	v1, v2, v3 := "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("e", 64), "sha256:"+strings.Repeat("f", 64)
	m.Fixture.ReleaseDigests = map[string]string{"release-a": v1, "release-b": v2, "release-c": v3}
	m.Phases.BeforeReboot = []Step{stepRelease(t, "a-deploy", "release-a", v1, "succeeded")}
	m.Phases.BeforeReboot = append(m.Phases.BeforeReboot, stepObservation(t, m, "v1", "a-deploy", "release-a", v1, "op-a-deploy")...)
	m.Phases.BeforeReboot = append(m.Phases.BeforeReboot, stepRelease(t, "b-update", "release-b", v2, "succeeded"))
	m.Phases.BeforeReboot = append(m.Phases.BeforeReboot, stepObservation(t, m, "v2", "b-update", "release-b", v2, "op-b-update")...)
	m.Phases.BeforeReboot = append(m.Phases.BeforeReboot, stepRelease(t, "c-replacement", "release-c", v3, "failed"))
	m.Phases.BeforeReboot = append(m.Phases.BeforeReboot, stepObservation(t, m, "preserved-v2", "b-update", "release-b", v2, "op-b-update")...)
	m.Phases.BeforeReboot = append(m.Phases.BeforeReboot, drillStep(t, map[string]any{"id": "rollback", "action": "rollback", "app": "udp-a", "release_step": "a-deploy"}))
	m.Phases.BeforeReboot = append(m.Phases.BeforeReboot, stepObservation(t, m, "rollback-v1", "rollback", "release-a", v1, "op-rollback")...)
	l.ManifestHash = manifestHash(m)
	return m, l
}

type sequenceCommands struct {
	t         *testing.T
	manifest  Manifest
	calls     []CommandSpec
	mutations int
	current   string
	poll      string
	lost      bool
	refused   bool
}

func (s *sequenceCommands) runner() *scriptedRunner {
	return &scriptedRunner{run: s.run}
}

func (s *sequenceCommands) run(c CommandSpec) (CommandResult, error) {
	s.t.Helper()
	s.calls = append(s.calls, c)
	if c.Mutating {
		return s.apply(c)
	}
	if slices.Contains(c.Args, "--operation") {
		return fixtureEnvelope(s.t, "status", map[string]any{"operation": map[string]string{"id": c.Args[slices.Index(c.Args, "--operation")+1], "state": s.poll}}), nil
	}
	if slices.Contains(c.Args, "rollback") {
		i := slices.Index(c.Args, "--release")
		if i < 0 || c.Args[i+1] != "op-a-deploy" {
			s.t.Fatalf("rollback did not select original v1 operation: %+v", c)
		}
		return fixtureEnvelope(s.t, "rollback", map[string]string{"plan_id": "plan-rollback", "release_id": "op-a-deploy"}), nil
	}
	if slices.Contains(c.Args, "-endpoint") {
		return s.probe(c), nil
	}
	return fixtureEnvelope(s.t, "status", apps.Report{Apps: []apps.Status{sequenceStatus(s.manifest, s.current)}}), nil
}

func (s *sequenceCommands) apply(c CommandSpec) (CommandResult, error) {
	s.mutations++
	id := ""
	for _, candidate := range []string{"a-deploy", "b-update", "c-replacement", "rollback"} {
		if slices.Contains(c.Args, operationKey(s.manifest.RunID, candidate)) {
			id = candidate
		}
	}
	if id == "" {
		s.t.Fatalf("unexpected mutation key: %+v", c)
	}
	if s.lost {
		return CommandResult{}, errors.New("lost response")
	}
	if s.refused {
		return commandResult(`{"schema_version":1,"command":"brine apply","ok":false,"error":{"code":"conflict"}}`), nil
	}
	s.poll = "succeeded"
	if id == "c-replacement" {
		s.poll = "failed"
	} else {
		s.current = id
	}
	return fixtureEnvelope(s.t, "apply", map[string]string{"status": "accepted", "operation_id": "op-" + id}), nil
}

func (s *sequenceCommands) probe(c CommandSpec) CommandResult {
	flag := func(name string) string { return c.Args[slices.Index(c.Args, name)+1] }
	release := "release-a"
	if s.current == "b-update" {
		release = "release-b"
	}
	if flag("-release") != release {
		s.t.Fatalf("probe command release=%q, current fixture=%q", flag("-release"), release)
	}
	result := client.Result{Schema: protocol.SchemaVersion, Fixture: protocol.FixtureName, Endpoint: flag("-endpoint"), Vantage: flag("-vantage"), Nonce: flag("-nonce"), ExpectedRelease: release, Attempts: 1, AttemptTimeoutMs: 1000, OverallTimeoutMs: 5000, StartedAt: time.Now().UTC().Add(-time.Second), FinishedAt: time.Now().UTC(), Outcome: "ok", Response: &protocol.Response{Schema: protocol.SchemaVersion, Fixture: protocol.FixtureName, Release: release, Nonce: flag("-nonce")}}
	raw, err := json.Marshal(result)
	if err != nil {
		s.t.Fatal(err)
	}
	return commandResult(string(raw))
}

func sequenceStatus(m Manifest, id string) apps.Status {
	app := m.Apps[0]
	release := "release-a"
	if id == "b-update" {
		release = "release-b"
	}
	allocation := plan.UDPAllocation{Endpoint: plan.UDPEndpoint{BindAddress: app.Endpoint.BindAddress, HostPort: 4433}, ContainerPort: 4433, ClientAddress: app.Endpoint.ClientAddress, Exposure: spec.UDPExposure(app.Endpoint.Exposure)}
	incarnation := "sha256:" + strings.Repeat("d", 64)
	return apps.Status{App: app.Name, Current: &apps.Release{ID: "op-" + id, ImageDigest: m.Fixture.ReleaseDigests[release], Domains: []spec.Domain{spec.Domain(app.Domain)}, UDP: &allocation}, Health: apps.Health{Direct: "healthy", UnitActive: target.Known(true)}, UDP: &apps.UDPStatus{Protocol: "udp", Local: udpready.Ready, Remote: "unverified", Reservation: "active", Incarnation: incarnation, BindAddress: app.Endpoint.BindAddress, ClientAddress: app.Endpoint.ClientAddress, HostPort: 4433, ContainerPort: 4433, Exposure: spec.UDPExposure(app.Endpoint.Exposure), Reservations: []plan.UDPReservation{{ID: "reservation", App: app.Name, Incarnation: incarnation, Endpoint: allocation.Endpoint, State: "active"}}}}
}

func fixtureEnvelope(t *testing.T, command string, data any) CommandResult {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"schema_version": 1, "command": "brine " + command, "ok": true, "data": data})
	if err != nil {
		t.Fatal(err)
	}
	return commandResult(string(raw))
}

func TestStepReleaseSequence(t *testing.T) {
	m, l := releaseSequence(t)
	script := &sequenceCommands{t: t, manifest: m}
	runner := Runner{Manifest: m, Ledger: l, Commands: script.runner()}
	report, err := runner.RunPhase(t.Context(), PhaseBeforeReboot)
	if err != nil {
		t.Fatalf("single-manifest v1/update/rejected replacement/rollback sequence: %v", err)
	}
	if len(report.Steps) != 12 || script.mutations != 4 {
		t.Fatalf("steps=%d mutations=%d", len(report.Steps), script.mutations)
	}
	if report.Ledger.Entries[2].State != "failed" || report.Ledger.Entries[3].ReleaseID != "op-a-deploy" {
		t.Fatalf("lost negative/rollback identity: %+v", report.Ledger.Entries)
	}
	// Replay a completed rollback and its observations, not the now-obsolete v1/v2 prefix.
	calls := len(script.calls)
	for _, step := range m.Phases.BeforeReboot[9:] {
		if _, err := runner.runStep(t.Context(), &report.Ledger, step); err != nil {
			t.Fatal(err)
		}
	}
	if script.mutations != 4 || len(script.calls) != calls+2 {
		t.Fatalf("rollback replay issued another plan or mutation: %+v", script.calls[calls:])
	}
	if manifestHash(m) != l.ManifestHash {
		t.Fatal("sequence changed its immutable manifest")
	}
}

func TestStepReleaseRejectsCrossedObservations(t *testing.T) {
	for _, field := range []string{"image", "release-id", "incarnation", "proof-release", "proof-image", "proof-incarnation", "probe-release", "nonce", "vantage"} {
		t.Run(field, func(t *testing.T) { testCrossedObservation(t, field) })
	}
}

func testCrossedObservation(t *testing.T, field string) {
	t.Helper()
	m, l := releaseSequence(t)
	m.Phases.BeforeReboot = m.Phases.BeforeReboot[:3]
	status := sequenceStatus(m, "a-deploy")
	step := m.Phases.BeforeReboot[1]
	switch field {
	case "image":
		status.Current.ImageDigest = m.Fixture.ReleaseDigests["release-b"]
	case "release-id":
		status.Current.ID = "op-b-update"
	case "incarnation":
		status.UDP.Incarnation = "sha256:" + strings.Repeat("e", 64)
	case "proof-release", "proof-image", "proof-incarnation":
		proof, err := loadStrict[map[string]any](step.Proof, maxReceipt)
		if err != nil {
			t.Fatal(err)
		}
		key, value := "release", "release-b"
		if field == "proof-image" {
			key, value = "image_digest", m.Fixture.ReleaseDigests["release-b"]
		}
		if field == "proof-incarnation" {
			key, value = "incarnation", "sha256:"+strings.Repeat("e", 64)
		}
		proof[key] = value
		writeJSON(t, step.Proof, proof)
		m.Phases.BeforeReboot[1].ProofSHA256 = mustFileHash(t, step.Proof)
	}
	l.ManifestHash = manifestHash(m)
	script := &sequenceCommands{t: t, manifest: m}
	commands := &scriptedRunner{run: func(c CommandSpec) (CommandResult, error) {
		if slices.Contains(c.Args, "status") && !slices.Contains(c.Args, "--operation") {
			return fixtureEnvelope(t, "status", apps.Report{Apps: []apps.Status{status}}), nil
		}
		result, err := script.run(c)
		if !slices.Contains(c.Args, "-endpoint") {
			return result, err
		}
		var probe client.Result
		if err := json.Unmarshal(result.Stdout, &probe); err != nil {
			t.Fatal(err)
		}
		switch field {
		case "probe-release":
			probe.ExpectedRelease = "release-b"
			probe.Response.Release = "release-b"
		case "nonce":
			probe.Response.Nonce = "other"
		case "vantage":
			probe.Vantage = "other"
		}
		raw, err := json.Marshal(probe)
		return commandResult(string(raw)), err
	}}
	runner := Runner{Manifest: m, Ledger: l, Commands: commands}
	if _, err := runner.RunPhase(t.Context(), PhaseBeforeReboot); err == nil {
		t.Fatalf("accepted crossed %s", field)
	}
}

func TestStepReleaseRefusesWrongReferenceBeforeCommands(t *testing.T) {
	for _, source := range []string{"missing", "v1", "b-update", "c-replacement", "cross-app", "unpinned", "crossed-pin"} {
		t.Run(source, func(t *testing.T) { testWrongReleaseReference(t, source) })
	}
}

func testWrongReleaseReference(t *testing.T, source string) {
	t.Helper()
	m, l := releaseSequence(t)
	m.Phases.BeforeReboot[1].ReleaseStep = source
	switch source {
	case "cross-app":
		m.Phases.BeforeReboot[0].App = ""
		m.Phases.BeforeReboot[1].ReleaseStep = "a-deploy"
	case "unpinned":
		m.Phases.BeforeReboot[0].Expectation = nil
		m.Phases.BeforeReboot[1].ReleaseStep = "a-deploy"
	case "crossed-pin":
		m.Phases.BeforeReboot[0].Expectation.ImageDigest = m.Fixture.ReleaseDigests["release-b"]
		m.Phases.BeforeReboot[1].ReleaseStep = "a-deploy"
	}
	commands := &scriptedRunner{run: func(CommandSpec) (CommandResult, error) {
		t.Fatal("invalid prior-step reference invoked a command")
		return CommandResult{}, nil
	}}
	runner := Runner{Manifest: m, Ledger: l, Commands: commands}
	if _, err := runner.RunPhase(t.Context(), PhaseBeforeReboot); err == nil {
		t.Fatal("accepted wrong prior-step reference")
	}
}

func TestStepReleaseRefusesUnsettledSourceOnReplay(t *testing.T) {
	for _, state := range []string{"unknown", "accepted", "recovery_required", "rejected", "failed", "rolled_back", "missing", "wrong-app", "wrong-action", "wrong-key", "wrong-plan", "wrong-command", "no-operation"} {
		t.Run(state, func(t *testing.T) { testUnsettledReleaseSource(t, state) })
	}
}

func testUnsettledReleaseSource(t *testing.T, state string) {
	t.Helper()
	m, l := releaseSequence(t)
	runner := Runner{Manifest: m, Ledger: l, Commands: &scriptedRunner{run: func(CommandSpec) (CommandResult, error) {
		t.Fatal("unsettled source invoked a command")
		return CommandResult{}, nil
	}}}
	stored, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry := releaseEntry(runner, m.Phases.BeforeReboot[0], "op-a-deploy")
	switch state {
	case "wrong-app":
		entry.App = "udp-b"
	case "wrong-action":
		entry.Action = ActionResume
	case "wrong-key":
		entry.Key = "other"
	case "wrong-plan":
		entry.PlanID = "other"
		entry.CommandSHA256 = commandHash(runner.applyCommand(entry.PlanID, entry.Key))
	case "wrong-command":
		entry.CommandSHA256 = "other"
	case "no-operation":
		entry.OperationID = ""
	case "missing":
	default:
		entry.State = state
	}
	if state != "missing" {
		stored.Entries = []LedgerEntry{entry}
	}
	for _, index := range []int{1, 2, 9} {
		if _, err := runner.runStep(t.Context(), &stored, m.Phases.BeforeReboot[index]); err == nil {
			t.Fatalf("accepted %s reference in %s", state, m.Phases.BeforeReboot[index].ID)
		}
	}
}

func TestStepReleaseLostOrRefusedApplyDoesNotReplay(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprint(refused), func(t *testing.T) { testLostOrRefusedApply(t, refused) })
	}
}

func testLostOrRefusedApply(t *testing.T, refused bool) {
	t.Helper()
	m, l := releaseSequence(t)
	script := &sequenceCommands{t: t, manifest: m, lost: !refused, refused: refused}
	runner := Runner{Manifest: m, Ledger: l, Commands: script.runner()}
	for i := 0; i < 2; i++ {
		if _, err := runner.RunPhase(t.Context(), PhaseBeforeReboot); err == nil {
			t.Fatal("lost/refused apply passed")
		}
	}
	if script.mutations != 1 {
		t.Fatalf("replayed mutation %d times", script.mutations)
	}
}

func TestStepReleaseDryRunUsesReferencedRelease(t *testing.T) {
	m, l := releaseSequence(t)
	runner := Runner{Manifest: m, Ledger: l, DryRun: true}
	report, err := runner.RunPhase(t.Context(), PhaseBeforeReboot)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := net.JoinHostPort(m.Apps[0].Endpoint.ClientAddress, "4433")
	if !strings.Contains(report.Steps[5].Outcome, "-release release-b") || !strings.Contains(report.Steps[5].Outcome, endpoint) {
		t.Fatalf("wrong dry-run probe: %+v", report.Steps[5])
	}
}

func releaseEntry(runner Runner, step Step, operation string) LedgerEntry {
	key := operationKey(runner.Manifest.RunID, step.ID)
	return LedgerEntry{ID: step.ID, Action: step.Action, App: step.App, Key: key, PlanID: step.PlanID, OperationID: operation, State: "succeeded", CommandSHA256: commandHash(runner.applyCommand(step.PlanID, key))}
}

func TestStepReleaseCrossedPriorOperation(t *testing.T) {
	m, l := releaseSequence(t)
	m.Phases.BeforeReboot[4].ReleaseStep = "a-deploy"
	l.ManifestHash = manifestHash(m)
	script := &sequenceCommands{t: t, manifest: m}
	runner := Runner{Manifest: m, Ledger: l, Commands: script.runner()}
	report, err := runner.RunPhase(t.Context(), PhaseBeforeReboot)
	if err == nil || len(report.Steps) != 5 || script.mutations != 2 {
		t.Fatalf("crossed prior operation was not refused at v2 status: steps=%d mutations=%d error=%v", len(report.Steps), script.mutations, err)
	}
}

func TestStepReleaseRollbackLedgerRefusesCrossedTarget(t *testing.T) {
	m, l := releaseSequence(t)
	runner := Runner{Manifest: m, Ledger: l, Commands: &scriptedRunner{run: func(CommandSpec) (CommandResult, error) {
		t.Fatal("crossed rollback target invoked a command")
		return CommandResult{}, nil
	}}}
	stored, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	original := releaseEntry(runner, m.Phases.BeforeReboot[0], "op-a-deploy")
	rollback := releaseEntry(runner, m.Phases.BeforeReboot[9], "op-rollback")
	rollback.PlanID = "plan-rollback"
	rollback.CommandSHA256 = commandHash(runner.applyCommand(rollback.PlanID, rollback.Key))
	rollback.ReleaseID = "op-b-update"
	stored.Entries = []LedgerEntry{original, rollback}
	for _, index := range []int{9, 10, 11} {
		if _, err := runner.runStep(t.Context(), &stored, m.Phases.BeforeReboot[index]); err == nil {
			t.Fatalf("accepted crossed rollback target in %s", m.Phases.BeforeReboot[index].ID)
		}
	}
}

func TestStepReleaseManifestBinding(t *testing.T) {
	m, l := releaseSequence(t)
	runner := Runner{Manifest: m, Ledger: l}
	stored, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	stored.Entries = []LedgerEntry{releaseEntry(runner, m.Phases.BeforeReboot[0], "op-a-deploy")}
	// A valid but changed expectation cannot reuse the original bound ledger.
	m.Phases.BeforeReboot[0].Expectation = &ReleaseExpectation{Release: "release-b", ImageDigest: m.Fixture.ReleaseDigests["release-b"]}
	runner.Manifest = m
	if _, _, err := runner.releaseFor(stored, m.Phases.BeforeReboot[1]); err == nil {
		t.Fatal("changed manifest reused original ledger")
	}
}

func TestStepReleaseStrictManifestReader(t *testing.T) {
	m, _ := releaseSequence(t)
	path := filepath.Join(t.TempDir(), "manifest.json")
	writeJSON(t, path, m)
	if _, err := LoadManifest(path); err != nil {
		t.Fatalf("new manifest cannot be read: %v", err)
	}
	m.Schema = 1
	writeJSON(t, path, m)
	if _, err := LoadManifest(path); err == nil {
		t.Fatal("old manifest schema accepted")
	}
	m.Schema = ManifestSchema
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	app := fields["apps"].([]any)[0].(map[string]any)
	app["expected_release"] = "release-a"
	writeJSON(t, path, fields)
	if _, err := LoadManifest(path); err == nil {
		t.Fatal("obsolete app-wide expectation accepted")
	}
}

func TestStepReleaseCrossedPollDoesNotSettleSource(t *testing.T) {
	m, l := releaseSequence(t)
	m.ReadAttempts = 1
	l.ManifestHash = manifestHash(m)
	script := &sequenceCommands{t: t, manifest: m}
	commands := &scriptedRunner{run: func(c CommandSpec) (CommandResult, error) {
		if slices.Contains(c.Args, "--operation") {
			return fixtureEnvelope(t, "status", map[string]any{"operation": map[string]string{"id": "other-operation", "state": "succeeded"}}), nil
		}
		return script.run(c)
	}}
	runner := Runner{Manifest: m, Ledger: l, Commands: commands}
	if _, err := runner.RunPhase(t.Context(), PhaseBeforeReboot); err == nil {
		t.Fatal("crossed poll settled original operation")
	}
	stored, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Entries) != 1 || stored.Entries[0].State != "accepted" || stored.Entries[0].OperationID != "op-a-deploy" {
		t.Fatalf("crossed poll changed original operation: %+v", stored.Entries)
	}
	if _, err := runner.RunPhase(t.Context(), PhaseBeforeReboot); err == nil {
		t.Fatal("crossed poll allowed replay")
	}
	if script.mutations != 1 {
		t.Fatal("crossed poll resubmitted original mutation")
	}
}

func TestStepReleaseAbsenceProof(t *testing.T) {
	m, l := releaseSequence(t)
	m.Phases.BeforeReboot = m.Phases.BeforeReboot[:2]
	step := &m.Phases.BeforeReboot[1]
	proof, err := loadStrict[ProductionProof](step.Proof, maxReceipt)
	if err != nil {
		t.Fatal(err)
	}
	proof.Kind, proof.HTTPHealth, proof.Mapping.Present, proof.Socket = "absent", "absent", false, nil
	writeJSON(t, step.Proof, proof)
	step.ExpectedOutcome, step.ProofSHA256 = "absent", mustFileHash(t, step.Proof)
	l.ManifestHash = manifestHash(m)
	runner := Runner{Manifest: m, Ledger: l}
	stored, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	stored.Entries = []LedgerEntry{releaseEntry(runner, m.Phases.BeforeReboot[0], "op-a-deploy")}
	runner.Commands = &scriptedRunner{run: func(CommandSpec) (CommandResult, error) {
		return fixtureEnvelope(t, "status", apps.Report{Apps: []apps.Status{}}), nil
	}}
	if _, err := runner.runStatus(t.Context(), &stored, *step); err != nil {
		t.Fatalf("complete absence proof refused: %v", err)
	}
	proof.ReleaseID = "op-b-update"
	writeJSON(t, step.Proof, proof)
	step.ProofSHA256 = mustFileHash(t, step.Proof)
	runner.Manifest = m
	stored.ManifestHash = manifestHash(m)
	if _, err := runner.runStatus(t.Context(), &stored, *step); err == nil {
		t.Fatal("crossed release absence proof accepted")
	}
}

func TestStepReleaseRollbackRefusalDoesNotReplay(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprint(refused), func(t *testing.T) { testRollbackRefusal(t, refused) })
	}
}

func testRollbackRefusal(t *testing.T, refused bool) {
	t.Helper()
	m, l := releaseSequence(t)
	script := &sequenceCommands{t: t, manifest: m, lost: !refused, refused: refused}
	runner := Runner{Manifest: m, Ledger: l, Commands: script.runner()}
	stored, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	stored.Entries = []LedgerEntry{releaseEntry(runner, m.Phases.BeforeReboot[0], "op-a-deploy")}
	for i := 0; i < 2; i++ {
		if _, err := runner.runRollback(t.Context(), &stored, m.Phases.BeforeReboot[9]); err == nil {
			t.Fatal("unknown/refused rollback accepted")
		}
	}
	if len(script.calls) != 2 || script.mutations != 1 {
		t.Fatalf("rollback replay planned or applied again: calls=%d mutations=%d", len(script.calls), script.mutations)
	}
	if stored.Entries[1].ReleaseID != "op-a-deploy" || stored.Entries[1].Key != operationKey(m.RunID, "rollback") {
		t.Fatalf("lost rollback target or key: %+v", stored.Entries[1])
	}
}
