package brinedrill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestApplySelectsExpectedTerminalOutcome(t *testing.T) {
	for _, outcome := range []string{"failed", "rolled_back"} {
		t.Run(outcome, func(t *testing.T) { testExpectedApplyOutcome(t, outcome) })
	}
}

func testExpectedApplyOutcome(t *testing.T, outcome string) {
	t.Helper()
	manifest, ledger, cleanup := testManifest(t)
	defer cleanup()
	manifest.Phases.BeforeReboot = []Step{
		{ID: "failed-replacement", Action: ActionApply, App: "udp-a", PlanID: "sha256:" + strings.Repeat("a", 64), ExpectedOutcome: outcome},
		{ID: "previous-release", Action: ActionStatus, App: "udp-a"},
	}
	commands := &scriptedRunner{run: func(command CommandSpec) (CommandResult, error) {
		if command.Mutating {
			return commandResult(`{"schema_version":1,"command":"brine apply","ok":true,"data":{"status":"accepted","operation_id":"op-failure"},"error":null}`), nil
		}
		if strings.Contains(strings.Join(command.Args, " "), "--operation") {
			return commandResult(fmt.Sprintf(`{"schema_version":1,"command":"brine status","ok":true,"data":{"operation":{"id":"op-failure","state":%q}},"error":null}`, outcome)), nil
		}
		return commandResult(`{"schema_version":1,"command":"brine status","ok":true,"data":{"apps":[{"app":"udp-a"}]},"error":null}`), nil
	}}
	runner := Runner{Manifest: manifest, Ledger: ledger, Commands: commands}
	report, err := runner.RunPhase(t.Context(), PhaseBeforeReboot)
	if err != nil {
		t.Fatalf("expected %s replacement stopped the scenario: %v", outcome, err)
	}
	if len(report.Steps) != 2 || report.Steps[0].State != outcome || len(commands.calls) != 3 {
		t.Fatalf("scenario did not observe previous release after %s: %+v, calls %d", outcome, report, len(commands.calls))
	}
	stored, err := ledger.Load()
	if err != nil || len(stored.Entries) != 1 || stored.Entries[0].State != outcome {
		t.Fatalf("terminal outcome not retained: %+v, error %v", stored, err)
	}
	if _, err := runner.RunPhase(t.Context(), PhaseBeforeReboot); err != nil {
		t.Fatalf("recorded expected outcome could not be reused: %v", err)
	}
	if len(commands.calls) != 4 {
		t.Fatalf("recorded expected outcome replayed a mutation: %d calls", len(commands.calls))
	}
}

func TestCleanupRequiresEveryDeclaredApp(t *testing.T) {
	manifest, _, cleanup := testManifest(t)
	defer cleanup()
	manifest.Target.Kind = "pi"
	second := manifest.Apps[0]
	second.Name, second.Domain, second.Endpoint.HostPort = "udp-b", "udp-b.example.test", 4434
	manifest.Apps = append(manifest.Apps, second)
	manifest.OwnedResources = append(manifest.OwnedResources, OwnedResource{Kind: "app", ID: second.Name, App: second.Name, Owner: manifest.RunID})
	if err := manifest.Validate(); err == nil {
		t.Fatal("two-app setup accepted cleanup that only removes udp-a")
	}
	manifest.Phases.Cleanup = append([]Step{{ID: "remove-b", Action: ActionLifecycle, App: second.Name, LifecycleAction: "remove"}}, manifest.Phases.Cleanup...)
	if err := manifest.Validate(); err != nil {
		t.Fatalf("complete two-app cleanup refused: %v", err)
	}
}

func TestApplyRejectsUnexpectedTerminalOutcome(t *testing.T) {
	manifest, ledger, cleanup := testManifest(t)
	defer cleanup()
	manifest.Phases.BeforeReboot = []Step{{ID: "replacement", Action: ActionApply, App: "udp-a", PlanID: "sha256:" + strings.Repeat("a", 64), ExpectedOutcome: "rolled_back"}}
	commands := &scriptedRunner{run: func(command CommandSpec) (CommandResult, error) {
		if command.Mutating {
			return commandResult(`{"schema_version":1,"command":"brine apply","ok":true,"data":{"status":"accepted","operation_id":"op-unexpected"},"error":null}`), nil
		}
		return commandResult(`{"schema_version":1,"command":"brine status","ok":true,"data":{"operation":{"id":"op-unexpected","state":"succeeded"}},"error":null}`), nil
	}}
	runner := Runner{Manifest: manifest, Ledger: ledger, Commands: commands}
	if _, err := runner.RunPhase(context.Background(), PhaseBeforeReboot); err == nil {
		t.Fatal("unexpected successful replacement satisfied expected rollback")
	}
	if _, err := runner.RunPhase(t.Context(), PhaseBeforeReboot); err == nil {
		t.Fatal("recorded unexpected success satisfied expected rollback on replay")
	}
	if len(commands.calls) != 2 {
		t.Fatalf("unexpected terminal outcome replayed a mutation: %d calls", len(commands.calls))
	}
}

func TestManifestRefusesInvalidApplyOutcomeBeforeCommands(t *testing.T) {
	manifest, ledger, cleanup := testManifest(t)
	defer cleanup()
	manifest.Phases.BeforeReboot = []Step{{ID: "replacement", Action: ActionApply, App: "udp-a", PlanID: "sha256:" + strings.Repeat("a", 64), ExpectedOutcome: "unknown"}}
	commands := &scriptedRunner{run: func(CommandSpec) (CommandResult, error) {
		t.Fatal("invalid outcome invoked command")
		return CommandResult{}, nil
	}}
	runner := Runner{Manifest: manifest, Ledger: ledger, Commands: commands}
	if _, err := runner.RunPhase(t.Context(), PhaseBeforeReboot); err == nil {
		t.Fatal("accepted unknown as an expected terminal apply outcome")
	}
}

func TestMissingSetupReceiptStopsBeforeEffects(t *testing.T) {
	manifest, ledger, cleanup := testManifest(t)
	defer cleanup()
	manifest.Phases.BeforeReboot = []Step{{ID: "apply", Action: ActionApply, App: "udp-a", PlanID: "sha256:" + strings.Repeat("a", 64)}}
	if err := os.Remove(manifest.DependencyReceipts[0].Path); err != nil {
		t.Fatal(err)
	}
	commands := &scriptedRunner{run: func(CommandSpec) (CommandResult, error) {
		t.Fatal("missing setup receipt allowed an effect")
		return CommandResult{}, nil
	}}
	runner := Runner{Manifest: manifest, Ledger: ledger, Commands: commands}
	if _, err := runner.RunPhase(t.Context(), PhaseBeforeReboot); err == nil {
		t.Fatal("missing setup receipt accepted")
	}
	if _, err := os.Stat(ledger.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("setup refusal wrote ledger: %v", err)
	}
}

func TestInterruptedApplyRetainsKeyAndAllowsExplicitCleanup(t *testing.T) {
	manifest, ledger, cleanup := testManifest(t)
	defer cleanup()
	manifest.Phases.BeforeReboot = []Step{{ID: "apply", Action: ActionApply, App: "udp-a", PlanID: "sha256:" + strings.Repeat("a", 64)}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	commands := &scriptedRunner{run: func(CommandSpec) (CommandResult, error) {
		stored, err := ledger.Load()
		if err != nil || len(stored.Entries) != 1 || stored.Entries[0].Key == "" || stored.Entries[0].State != "prepared" {
			t.Fatalf("apply started before durable preparation: %+v, %v", stored, err)
		}
		cancel()
		return CommandResult{}, ctx.Err()
	}}
	runner := Runner{Manifest: manifest, Ledger: ledger, Commands: commands}
	if _, err := runner.RunPhase(ctx, PhaseBeforeReboot); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted apply error = %v", err)
	}
	stored, err := ledger.Load()
	if err != nil || len(stored.Entries) != 1 || stored.Entries[0].State != "unknown" {
		t.Fatalf("interrupted apply ledger = %+v, %v", stored, err)
	}
	key := stored.Entries[0].Key
	if _, err := runner.RunPhase(t.Context(), PhaseBeforeReboot); err == nil || len(commands.calls) != 1 {
		t.Fatalf("interrupted apply replayed: %v, calls %d", err, len(commands.calls))
	}
	runner.Commands = cleanupCommands("succeeded")
	if _, err := runner.RunPhase(t.Context(), PhaseCleanup); err != nil {
		t.Fatalf("explicit cleanup after interrupted setup: %v", err)
	}
	stored, err = ledger.Load()
	if err != nil || stored.Entries[0].Key != key || stored.Entries[0].State != "unknown" {
		t.Fatalf("cleanup lost interrupted apply identity: %+v, %v", stored, err)
	}
	assertResourceState(t, stored, "absent")
}

func TestCleanupFailureDoesNotClaimAbsence(t *testing.T) {
	manifest, ledger, cleanup := testManifest(t)
	defer cleanup()
	commands := cleanupCommands("recovery_required")
	runner := Runner{Manifest: manifest, Ledger: ledger, Commands: commands}
	if _, err := runner.RunPhase(t.Context(), PhaseCleanup); err == nil {
		t.Fatal("unsettled removal accepted as teardown")
	}
	stored, err := ledger.Load()
	if err != nil {
		t.Fatal(err)
	}
	assertResourceState(t, stored, "planned")
	if len(commands.calls) != 3 {
		t.Fatalf("cleanup made extra calls after unsettled removal: %d", len(commands.calls))
	}
}

func cleanupCommands(outcome string) *scriptedRunner {
	return &scriptedRunner{run: func(command CommandSpec) (CommandResult, error) {
		if command.Mutating {
			return commandResult(`{"schema_version":1,"command":"brine apply","ok":true,"data":{"status":"accepted","operation_id":"op-remove"},"error":null}`), nil
		}
		if strings.Contains(strings.Join(command.Args, " "), "--operation") {
			return commandResult(fmt.Sprintf(`{"schema_version":1,"command":"brine status","ok":true,"data":{"operation":{"id":"op-remove","state":%q}},"error":null}`, outcome)), nil
		}
		return commandResult(`{"schema_version":1,"command":"brine remove","ok":true,"data":{"plan_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"error":null}`), nil
	}}
}

func assertResourceState(t *testing.T, ledger Ledger, expected string) {
	t.Helper()
	for _, resource := range ledger.Resources {
		if resource.State != expected {
			t.Fatalf("resource %s state = %s, want %s", resource.ID, resource.State, expected)
		}
	}
}
