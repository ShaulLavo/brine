package brinedrill

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/udpready"
)

type scriptedRunner struct {
	calls []CommandSpec
	run   func(CommandSpec) (CommandResult, error)
}

func (r *scriptedRunner) Run(_ context.Context, command CommandSpec, _ time.Duration, _ int) (CommandResult, error) {
	r.calls = append(r.calls, command)
	return r.run(command)
}

func TestApplyDoesNotReplayAfterResponseLoss(t *testing.T) {
	manifest, ledger, cleanup := testManifest(t)
	defer cleanup()
	manifest.Phases.BeforeReboot = []Step{{ID: "apply-a", Action: ActionApply, App: "udp-a", PlanID: "sha256:" + strings.Repeat("a", 64)}}
	commands := &scriptedRunner{run: func(CommandSpec) (CommandResult, error) {
		return CommandResult{}, errors.New("response lost")
	}}
	runner := Runner{Manifest: manifest, Ledger: ledger, Commands: commands}
	if _, err := runner.RunPhase(context.Background(), PhaseBeforeReboot); err == nil || !strings.Contains(err.Error(), "recovery-required") {
		t.Fatalf("first apply error = %v", err)
	}
	if _, err := runner.RunPhase(context.Background(), PhaseBeforeReboot); err == nil || !strings.Contains(err.Error(), "inspection") {
		t.Fatalf("replayed apply error = %v", err)
	}
	if len(commands.calls) != 1 {
		t.Fatalf("calls = %d, want one persisted attempt", len(commands.calls))
	}
	stored, err := ledger.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Entries) != 1 || stored.Entries[0].State != "unknown" || stored.Entries[0].Key == "" {
		t.Fatalf("ledger = %+v", stored)
	}
}

func TestApplySuccessUsesReviewedEnvelopeShapes(t *testing.T) {
	manifest, ledger, cleanup := testManifest(t)
	defer cleanup()
	manifest.Phases.BeforeReboot = []Step{{ID: "apply-a", Action: ActionApply, App: "udp-a", PlanID: "sha256:" + strings.Repeat("a", 64)}}
	commands := &scriptedRunner{run: func(command CommandSpec) (CommandResult, error) {
		joined := strings.Join(command.Args, " ")
		if strings.Contains(joined, " apply ") {
			return commandResult(`{"schema_version":1,"command":"brine apply","ok":true,"data":{"status":"accepted","operation_id":"op-1"},"error":null}`), nil
		}
		return commandResult(`{"schema_version":1,"command":"brine status","ok":true,"data":{"operation":{"state":"succeeded"}},"error":null}`), nil
	}}
	runner := Runner{Manifest: manifest, Ledger: ledger, Commands: commands}
	if _, err := runner.RunPhase(context.Background(), PhaseBeforeReboot); err != nil {
		t.Fatal(err)
	}
	stored, err := ledger.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Entries) != 1 || stored.Entries[0].State != "succeeded" || stored.Entries[0].OperationID != "op-1" {
		t.Fatalf("ledger = %+v", stored)
	}
	if len(commands.calls) != 2 || !strings.Contains(strings.Join(commands.calls[0].Args, " "), "--idempotency-key") || !strings.Contains(strings.Join(commands.calls[1].Args, " "), "--operation op-1") {
		t.Fatalf("commands = %+v", commands.calls)
	}
}

func TestKnownBrineRefusalIsNotMarkedUnknown(t *testing.T) {
	manifest, ledger, cleanup := testManifest(t)
	defer cleanup()
	manifest.Phases.BeforeReboot = []Step{{ID: "apply-a", Action: ActionApply, App: "udp-a", PlanID: "sha256:" + strings.Repeat("a", 64)}}
	commands := &scriptedRunner{run: func(CommandSpec) (CommandResult, error) {
		return commandResult(`{"schema_version":1,"command":"brine apply","ok":false,"data":null,"error":{"code":"conflict","message":"refused","retryable":true}}`), nil
	}}
	if _, err := (func() (RunReport, error) {
		runner := Runner{Manifest: manifest, Ledger: ledger, Commands: commands}
		return runner.RunPhase(context.Background(), PhaseBeforeReboot)
	})(); err == nil {
		t.Fatal("accepted refused apply")
	}
	stored, err := ledger.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Entries) != 1 || stored.Entries[0].State != "rejected" {
		t.Fatalf("refusal ledger = %+v", stored)
	}
}

func commandResult(raw string) CommandResult {
	return CommandResult{Stdout: []byte(raw), ExitCode: 0}
}

func TestTeardownProofMarksOnlyOwnedResourcesAbsent(t *testing.T) {
	manifest, ledger, cleanup := testManifest(t)
	defer cleanup()
	proofPath := filepath.Join(filepath.Dir(ledger.Path), "teardown.json")
	proof := TeardownProof{Schema: ReceiptSchema, RunID: manifest.RunID, Target: manifest.Target.Name, ForeignUntouched: true, Resources: []TeardownResource{{Kind: "app", ID: "udp-a", State: "absent"}, {Kind: "udp_endpoint", ID: "udp-a:4433", State: "absent"}, {Kind: "image", ID: manifest.Fixture.ReleaseDigests["release-a"], State: "absent"}, {Kind: "grant", ID: "udp-a-grant", State: "absent"}}}
	writeJSON(t, proofPath, proof)
	manifest.Phases.Cleanup = []Step{{ID: "teardown", Action: ActionTeardownProof, Proof: proofPath}}
	runner := Runner{Manifest: manifest, Ledger: ledger, Commands: &scriptedRunner{run: func(CommandSpec) (CommandResult, error) {
		t.Fatal("teardown proof invoked a command")
		return CommandResult{}, nil
	}}}
	if _, err := runner.RunPhase(context.Background(), PhaseCleanup); err != nil {
		t.Fatal(err)
	}
	stored, err := ledger.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range stored.Resources {
		if resource.State != "absent" {
			t.Fatalf("resource %s remains %s", resource.ID, resource.State)
		}
	}
	proof.ForeignUntouched = false
	writeJSON(t, proofPath, proof)
	badRunner := Runner{Manifest: manifest, Ledger: ledger, Commands: &scriptedRunner{run: func(CommandSpec) (CommandResult, error) { return CommandResult{}, nil }}}
	if _, err := badRunner.RunPhase(context.Background(), PhaseCleanup); err == nil {
		t.Fatal("accepted teardown proof that did not preserve foreign resources")
	}
}

func TestManifestRefusesMissingPinsAndUnsafeActions(t *testing.T) {
	manifest, _, cleanup := testManifest(t)
	defer cleanup()
	manifest.Fixture.ImageIndexDigest = ""
	if err := manifest.Validate(); err == nil {
		t.Fatal("accepted missing image index")
	}
	manifest, _, cleanup = testManifest(t)
	defer cleanup()
	manifest.Phases.BeforeReboot = []Step{{ID: "bad", Action: Action("reboot")}}
	if err := manifest.Validate(); err == nil {
		t.Fatal("accepted unsupported reboot action")
	}
	manifest, _, cleanup = testManifest(t)
	defer cleanup()
	manifest.Apps[0].Endpoint.BindAddress = "0.0.0.0"
	if err := manifest.Validate(); err == nil {
		t.Fatal("accepted wildcard bind")
	}
}

func TestAfterRebootGateRunsBeforeCommands(t *testing.T) {
	manifest, ledger, cleanup := testManifest(t)
	defer cleanup()
	afterPath := manifest.PhaseReceipts.AfterReboot.Path
	after := Receipt{Schema: ReceiptSchema, RunID: manifest.RunID, Target: manifest.Target.Name, Phase: "after-reboot", HostProof: "host", RebootRestored: false}
	writeJSON(t, afterPath, after)
	manifest.Phases.AfterReboot = []Step{{ID: "status", Action: ActionStatus, App: "udp-a", ExpectedOutcome: "stopped"}}
	commands := &scriptedRunner{run: func(CommandSpec) (CommandResult, error) {
		t.Fatal("after-reboot command ran past gate")
		return CommandResult{}, nil
	}}
	runner := Runner{Manifest: manifest, Ledger: ledger, Commands: commands}
	if _, err := runner.RunPhase(context.Background(), PhaseAfterReboot); err == nil {
		t.Fatal("accepted un-restored after-reboot receipt")
	}
	if len(commands.calls) != 0 {
		t.Fatal("phase gate did not stop command execution")
	}
}

func TestReadyProofPreservesHTTPAndAppIdentity(t *testing.T) {
	manifest, ledger, cleanup := testManifest(t)
	defer cleanup()
	app := manifest.Apps[0]
	status := apps.Status{
		App:     app.Name,
		Current: &apps.Release{ImageDigest: app.ImageDigest, Domains: []spec.Domain{spec.Domain(app.Domain)}, UDP: &plan.UDPAllocation{Endpoint: plan.UDPEndpoint{BindAddress: app.Endpoint.BindAddress, HostPort: target.Port(app.Endpoint.HostPort)}, ContainerPort: target.Port(app.Endpoint.ContainerPort), ClientAddress: app.Endpoint.ClientAddress, Exposure: spec.UDPExposure(app.Endpoint.Exposure)}},
		Health:  apps.Health{Direct: "healthy", UnitActive: target.Known(true)},
		UDP:     &apps.UDPStatus{Local: udpready.Ready, Remote: "unverified", Reservation: "active", BindAddress: app.Endpoint.BindAddress, ClientAddress: app.Endpoint.ClientAddress, HostPort: target.Port(app.Endpoint.HostPort), ContainerPort: target.Port(app.Endpoint.ContainerPort), Exposure: spec.UDPExposure(app.Endpoint.Exposure)},
	}
	proofPath := filepath.Join(filepath.Dir(ledger.Path), "ready.json")
	proof := ProductionProof{Schema: ReceiptSchema, RunID: manifest.RunID, Target: manifest.Target.Name, Kind: "ready", App: app.Name, Endpoint: app.Endpoint, Release: app.ExpectedRelease, ImageDigest: app.ImageDigest, HTTPHealth: "healthy", NetworkMode: "pasta", Mapping: MappingProof{Present: true, BindAddress: app.Endpoint.BindAddress, HostPort: app.Endpoint.HostPort, ContainerPort: app.Endpoint.ContainerPort}, Socket: &SocketProof{Address: app.Endpoint.BindAddress, Port: app.Endpoint.HostPort, App: app.Name, Process: "pasta", Unit: app.Name + ".service", Inode: 42, Source: "ss -H -lunpe", Ownership: "affirmed"}, InodeGate: "confirmed", ForeignOverlap: "none", RouteUnchanged: true}
	writeJSON(t, proofPath, proof)
	if err := validateReady(status, &app, proofPath); err != nil {
		t.Fatal(err)
	}
	status.Health.Direct = "unhealthy"
	if err := validateReady(status, &app, proofPath); err == nil {
		t.Fatal("accepted unhealthy HTTP alongside UDP proof")
	}
}

func TestDryRunHasNoCommandOrLedgerEffects(t *testing.T) {
	manifest, ledger, cleanup := testManifest(t)
	defer cleanup()
	manifest.Phases.BeforeReboot = []Step{{ID: "status", Action: ActionStatus, App: "udp-a"}}
	commands := &scriptedRunner{run: func(CommandSpec) (CommandResult, error) {
		t.Fatal("dry-run invoked command")
		return CommandResult{}, nil
	}}
	runner := Runner{Manifest: manifest, Ledger: ledger, Commands: commands, DryRun: true}
	if _, err := runner.RunPhase(context.Background(), PhaseBeforeReboot); err != nil {
		t.Fatal(err)
	}
	if len(commands.calls) != 0 {
		t.Fatal("dry-run invoked a command")
	}
	if _, err := os.Stat(ledger.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote ledger, stat error = %v", err)
	}
}

func testManifest(t *testing.T) (Manifest, FileLedger, func()) {
	t.Helper()
	dir := t.TempDir()
	brinePath := filepath.Join(dir, "brine")
	clientPath := filepath.Join(dir, "client")
	if err := os.WriteFile(brinePath, []byte("brine"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientPath, []byte("client"), 0600); err != nil {
		t.Fatal(err)
	}
	brineHash := mustFileHash(t, brinePath)
	clientHash := mustFileHash(t, clientPath)
	commit := strings.Repeat("a", 40)
	image := "sha256:" + strings.Repeat("b", 64)
	releaseImage := "sha256:" + strings.Repeat("c", 64)
	beforePath := filepath.Join(dir, "before.json")
	afterPath := filepath.Join(dir, "after.json")
	before := Receipt{Schema: ReceiptSchema, RunID: "drill-1", Target: "vps", Phase: "before-reboot", HostProof: "host"}
	after := Receipt{Schema: ReceiptSchema, RunID: "drill-1", Target: "vps", Phase: "after-reboot", HostProof: "host", RebootRestored: true}
	writeJSON(t, beforePath, before)
	writeJSON(t, afterPath, after)
	manifest := Manifest{Schema: ManifestSchema, RunID: "drill-1", Source: SourcePins{Commit: commit, HostBinary: BinaryPin{Path: brinePath, SHA256: brineHash, SourceCommit: commit}, ClientBinary: BinaryPin{Path: clientPath, SHA256: clientHash, SourceCommit: commit}}, Fixture: FixturePins{ImageIndexDigest: image, PlatformDigests: map[string]string{"linux/amd64": image, "linux/arm64": image}, ReleaseDigests: map[string]string{"release-a": releaseImage}}, Target: TargetSpec{Kind: "vps", Name: "vps", ConfigDir: dir, ControlledUnreachable: "198.51.100.10:65530"}, Apps: []AppSpec{{Name: "udp-a", Domain: "udp-a.example.test", ExpectedRelease: "release-a", ImageDigest: releaseImage, Endpoint: Endpoint{BindAddress: "192.0.2.10", ClientAddress: "192.0.2.10", HostPort: 4433, ContainerPort: 4433, Exposure: "public"}}}, PhaseReceipts: PhaseReceipts{BeforeReboot: ReceiptRef{Name: "before", Path: beforePath, SHA256: mustFileHash(t, beforePath)}, AfterReboot: ReceiptRef{Name: "after", Path: afterPath, SHA256: mustFileHash(t, afterPath)}}, DependencyReceipts: []ReceiptRef{{Name: "source", Path: beforePath, SHA256: mustFileHash(t, beforePath)}}, OwnedResources: []OwnedResource{{Kind: "app", ID: "udp-a", App: "udp-a", Owner: "drill-1"}, {Kind: "udp_endpoint", ID: "udp-a:4433", App: "udp-a", Owner: "drill-1"}, {Kind: "image", ID: releaseImage, App: "udp-a", Owner: "drill-1"}, {Kind: "grant", ID: "udp-a-grant", App: "udp-a", Owner: "drill-1"}}}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(dir, "ledger.json")
	ledger := FileLedger{Path: ledgerPath, RunID: manifest.RunID, ManifestHash: manifestHash(manifest), Resources: manifest.OwnedResources}
	return manifest, ledger, func() {}
}

func mustFileHash(t *testing.T, path string) string {
	t.Helper()
	value, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
