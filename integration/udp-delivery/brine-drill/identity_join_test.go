package brinedrill

import (
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
)

func TestReviewPollIdentityJoin(t *testing.T) {
	for _, field := range []string{"app", "plan_id", "kind"} {
		t.Run(field, func(t *testing.T) { reviewPollIdentity(t, field) })
	}
}

func reviewPollIdentity(t *testing.T, field string) {
	t.Helper()
	m, l := releaseSequence(t)
	m.Phases.BeforeReboot = m.Phases.BeforeReboot[:3]
	m.ReadAttempts = 1
	l.ManifestHash = manifestHash(m)
	s := &sequenceCommands{t: t, manifest: m}
	commands := &scriptedRunner{run: func(c CommandSpec) (CommandResult, error) {
		if !slices.Contains(c.Args, "--operation") {
			return s.run(c)
		}
		op := map[string]string{"id": "op-a-deploy", "state": "succeeded", "app": "udp-a", "plan_id": m.Phases.BeforeReboot[0].PlanID, "kind": "deploy"}
		switch field {
		case "app":
			op[field] = "udp-b"
		case "plan_id":
			op[field] = "sha256:" + strings.Repeat("e", 64)
		case "kind":
			op[field] = "reconcile"
		}
		return fixtureEnvelope(t, "status", map[string]any{"operation": op}), nil
	}}
	runner := Runner{Manifest: m, Ledger: l, Commands: commands}
	report, err := runner.RunPhase(t.Context(), PhaseBeforeReboot)
	if err == nil {
		t.Fatalf("crossed operation %s accepted through %d steps including ready/probe; ledger=%+v", field, len(report.Steps), report.Ledger.Entries)
	}
}

func TestReviewReadyPlanJoin(t *testing.T) {
	m, l := releaseSequence(t)
	m.Phases.BeforeReboot = m.Phases.BeforeReboot[:3]
	l.ManifestHash = manifestHash(m)
	s := &sequenceCommands{t: t, manifest: m}
	commands := &scriptedRunner{run: func(c CommandSpec) (CommandResult, error) {
		if !slices.Contains(c.Args, "status") || slices.Contains(c.Args, "--operation") {
			return s.run(c)
		}
		status := sequenceStatus(m, "a-deploy")
		status.Current.PlanID = "sha256:" + strings.Repeat("f", 64)
		return fixtureEnvelope(t, "status", map[string]any{"apps": []any{status}}), nil
	}}
	runner := Runner{Manifest: m, Ledger: l, Commands: commands}
	report, err := runner.RunPhase(t.Context(), PhaseBeforeReboot)
	if err == nil {
		t.Fatalf("crossed ready release plan accepted through %d steps including independent proof/probe", len(report.Steps))
	}
}

func TestReviewPlanSourceLedgerJoin(t *testing.T) {
	for _, field := range []string{"valid", "app", "action", "key", "command"} {
		t.Run(field, func(t *testing.T) { reviewPlanSource(t, field) })
	}
}

func reviewPlanSource(t *testing.T, field string) {
	t.Helper()
	m, l := releaseSequence(t)
	source := m.Phases.BeforeReboot[0]
	planID := source.PlanID
	source.PlanID, source.PlanStep = "", "review-plan"
	planned := Step{ID: "review-plan", Action: ActionPlan, App: source.App, Spec: m.Source.HostBinary.Path}
	m.Phases.BeforeReboot = append([]Step{planned, source}, m.Phases.BeforeReboot[1:]...)
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	l.ManifestHash = manifestHash(m)
	runner := Runner{Manifest: m, Ledger: l}
	stored, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := LedgerEntry{ID: planned.ID, Action: planned.Action, App: planned.App, Key: operationKey(m.RunID, planned.ID), PlanID: planID, State: "succeeded", CommandSHA256: commandHash(runner.planCommand(planned))}
	switch field {
	case "app":
		p.App = "foreign"
	case "action":
		p.Action = ActionResume
	case "key":
		p.Key = "foreign"
	case "command":
		p.CommandSHA256 = "foreign"
	}
	applied := releaseEntry(runner, source, "op-a-deploy")
	applied.PlanID = planID
	applied.CommandSHA256 = commandHash(runner.applyCommand(planID, applied.Key))
	stored.Entries = []LedgerEntry{p, applied}
	_, _, err = runner.releaseFor(stored, m.Phases.BeforeReboot[2])
	if field == "valid" && err != nil {
		t.Fatal(err)
	}
	if field != "valid" && err == nil {
		t.Fatalf("release source accepted crossed plan-entry %s", field)
	}
}

func TestReviewForwardPlanSource(t *testing.T) {
	m, _ := releaseSequence(t)
	m.Phases.BeforeReboot[0].PlanID = ""
	m.Phases.BeforeReboot[0].PlanStep = "later-plan"
	m.Phases.BeforeReboot = append(m.Phases.BeforeReboot, Step{ID: "later-plan", Action: ActionPlan, App: "udp-a", Spec: m.Source.HostBinary.Path})
	if err := m.Validate(); err == nil {
		t.Fatal("forward plan source passed manifest validation")
	}
}

func TestReviewReleaseDAGControls(t *testing.T) {
	for _, ref := range []string{"a-deploy", "v1", "b-update", "c-replacement", "rollback", "not-declared"} {
		t.Run(ref, func(t *testing.T) { reviewReleaseDAG(t, ref) })
	}
}

func reviewReleaseDAG(t *testing.T, ref string) {
	t.Helper()
	m, _ := releaseSequence(t)
	m.Phases.BeforeReboot[1].ReleaseStep = ref
	err := m.Validate()
	if ref == "a-deploy" && err != nil {
		t.Fatal(err)
	}
	if ref != "a-deploy" && err == nil {
		t.Fatalf("accepted non-prior release source %s", ref)
	}
}

func TestPollMissingIdentityKeepsAcceptedSource(t *testing.T) {
	for _, field := range []string{"app", "plan_id", "kind"} {
		t.Run(field, func(t *testing.T) { testPollMissingIdentity(t, field) })
	}
}

func testPollMissingIdentity(t *testing.T, field string) {
	t.Helper()
	m, l := releaseSequence(t)
	m.Phases.BeforeReboot = m.Phases.BeforeReboot[:3]
	m.ReadAttempts = 1
	l.ManifestHash = manifestHash(m)
	s := &sequenceCommands{t: t, manifest: m}
	commands := &scriptedRunner{run: func(c CommandSpec) (CommandResult, error) {
		if !slices.Contains(c.Args, "--operation") {
			return s.run(c)
		}
		op := map[string]string{"id": "op-a-deploy", "state": "succeeded", "app": "udp-a", "plan_id": m.Phases.BeforeReboot[0].PlanID, "kind": "deploy"}
		delete(op, field)
		return fixtureEnvelope(t, "status", map[string]any{"operation": op}), nil
	}}
	runner := Runner{Manifest: m, Ledger: l, Commands: commands}
	if _, err := runner.RunPhase(t.Context(), PhaseBeforeReboot); err == nil {
		t.Fatal("missing operation binding settled source")
	}
	stored, err := l.Load()
	if err != nil || len(stored.Entries) != 1 || stored.Entries[0].State != "accepted" {
		t.Fatalf("source changed: %+v, %v", stored, err)
	}
	if _, err := runner.RunPhase(t.Context(), PhaseBeforeReboot); err == nil || s.mutations != 1 {
		t.Fatalf("contradictory source replayed mutation: %d, %v", s.mutations, err)
	}
}

func TestPlanApplyObservationUsesExactPlanEntry(t *testing.T) {
	m, l := releaseSequence(t)
	source := m.Phases.BeforeReboot[0]
	planID := source.PlanID
	source.PlanID, source.PlanStep = "", "source-plan"
	planned := Step{ID: "source-plan", Action: ActionPlan, App: source.App, Spec: m.Source.HostBinary.Path}
	m.Phases.BeforeReboot = append([]Step{planned, source}, m.Phases.BeforeReboot[1:3]...)
	l.ManifestHash = manifestHash(m)
	s := &sequenceCommands{t: t, manifest: m}
	commands := &scriptedRunner{run: func(c CommandSpec) (CommandResult, error) {
		if slices.Contains(c.Args, "plan") {
			return fixtureEnvelope(t, "plan", map[string]string{"plan_id": planID}), nil
		}
		return s.run(c)
	}}
	runner := Runner{Manifest: m, Ledger: l, Commands: commands}
	report, err := runner.RunPhase(t.Context(), PhaseBeforeReboot)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Steps) != 4 || report.Ledger.Entries[0].CommandSHA256 != commandHash(runner.planCommand(planned)) {
		t.Fatalf("plan identity lost: %+v", report.Ledger.Entries)
	}
}

func TestIndirectPlanManifestRefusals(t *testing.T) {
	for _, field := range []string{"missing", "forward", "action", "app", "ambiguous"} {
		t.Run(field, func(t *testing.T) { testIndirectPlanManifest(t, field) })
	}
}

func testIndirectPlanManifest(t *testing.T, field string) {
	t.Helper()
	m, _ := releaseSequence(t)
	source := m.Phases.BeforeReboot[0]
	planned := Step{ID: "source-plan", Action: ActionPlan, App: source.App, Spec: m.Source.HostBinary.Path}
	source.PlanID, source.PlanStep = "", planned.ID
	switch field {
	case "action":
		planned.Action = ActionStatus
	case "app":
		planned.App = "another-app"
	case "ambiguous":
		source.PlanID = "sha256:" + strings.Repeat("a", 64)
	}
	m.Phases.BeforeReboot = append([]Step{planned, source}, m.Phases.BeforeReboot[1:]...)
	if field == "missing" {
		m.Phases.BeforeReboot = m.Phases.BeforeReboot[1:]
	}
	if field == "forward" {
		m.Phases.BeforeReboot[0], m.Phases.BeforeReboot[1] = m.Phases.BeforeReboot[1], m.Phases.BeforeReboot[0]
	}
	if err := m.Validate(); err == nil {
		t.Fatal("invalid indirect selection admitted")
	}
}

func TestReadyPlanIdentityForApplyAndRollback(t *testing.T) {
	for _, pair := range [][2]string{{"a-deploy", ""}, {"a-deploy", "different-plan"}, {"rollback", ""}, {"rollback", "different-plan"}} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) { testReadyPlanIdentity(t, pair[0], pair[1]) })
	}
}

func testReadyPlanIdentity(t *testing.T, id, planID string) {
	t.Helper()
	m, l := releaseSequence(t)
	s := &sequenceCommands{t: t, manifest: m}
	commands := &scriptedRunner{run: func(c CommandSpec) (CommandResult, error) {
		if !slices.Contains(c.Args, "status") || slices.Contains(c.Args, "--operation") || s.current != id {
			return s.run(c)
		}
		status := sequenceStatus(m, id)
		status.Current.PlanID = planID
		return fixtureEnvelope(t, "status", map[string]any{"apps": []any{status}}), nil
	}}
	runner := Runner{Manifest: m, Ledger: l, Commands: commands}
	if _, err := runner.RunPhase(t.Context(), PhaseBeforeReboot); err == nil {
		t.Fatal("missing or crossed ready release plan accepted")
	}
}

func TestResolveTracksItsOwnOperationIdentity(t *testing.T) {
	m, l := releaseSequence(t)
	stored, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry := releaseEntry(Runner{Manifest: m}, m.Phases.BeforeReboot[0], "op-original")
	entry.State = "recovery_required"
	stored.Entries = []LedgerEntry{entry}
	commands := &scriptedRunner{run: func(c CommandSpec) (CommandResult, error) {
		if c.Mutating {
			return fixtureEnvelope(t, "resolve", map[string]string{"status": "accepted", "operation_id": "op-resolver"}), nil
		}
		return fixtureEnvelope(t, "status", map[string]any{"operation": map[string]string{"id": "op-resolver", "app": entry.App, "plan_id": entry.PlanID, "kind": "resolve", "recovery_of": entry.OperationID, "state": "succeeded"}}), nil
	}}
	r := Runner{Manifest: m, Ledger: l, Commands: commands}
	if _, err := r.resolve(t.Context(), &stored, Step{ID: "resume", Action: ActionResume, OperationID: entry.OperationID}); err != nil {
		t.Fatal(err)
	}
	resolved := stored.Entries[0]
	if resolved.OperationKind != ops.Resolve || resolved.RecoveryOf != entry.OperationID || resolved.OperationID != "op-resolver" {
		t.Fatalf("resolver identity lost: %+v", resolved)
	}
	if _, err := r.runInspect(t.Context(), &stored, Step{ID: "inspect", Action: ActionInspect, OperationID: "op-resolver"}); err != nil {
		t.Fatal(err)
	}
}
