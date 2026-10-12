package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestPublicArchiveCLIUsesDetachedTypedOutcome(t *testing.T) {
	for _, noWait := range []bool{false, true} {
		t.Run(map[bool]string{false: "wait", true: "accepted"}[noWait], func(t *testing.T) { checkPublicArchiveCLI(t, noWait) })
	}
}
func checkPublicArchiveCLI(t *testing.T, noWait bool) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	archiveID, key, plan := strings.Repeat("a", 32), strings.Repeat("b", 32), "sha256:"+strings.Repeat("c", 64)
	receipt := archive.MaintenanceReceipt{Kind: string(ops.ArchiveMaintenanceApply), OperationID: "archive-maintenance-operation", PlanID: plan, ArchiveID: data.ArchiveID(archiveID), MaintenanceKind: archive.ExpireArchive, Outcome: "local_deleted", EvidenceSHA256: "sha256:" + strings.Repeat("d", 64), CompletedAt: time.Now().UTC()}
	calls, polls := 0, 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, r dispatch.Request) (result.Envelope, error) {
		if r.Op == "operation" {
			polls++
			raw, _ := json.Marshal(receipt)
			return result.Success("brine host operation", jobs.Status{Operation: ops.Operation{ID: receipt.OperationID, Kind: ops.ArchiveMaintenanceApply, App: "example", SecretRef: plan, State: ops.Succeeded}, Events: []ops.Event{}, Outcome: &ops.TaskOutcome{Receipt: raw}}), nil
		}
		calls++
		var a dispatch.ArchiveMaintenanceApplyArgs
		if r.Op != "archive_maintenance_apply" || json.Unmarshal(r.Args, &a) != nil || string(a.ArchiveID) != archiveID || a.PlanID != plan || a.IdempotencyKey != key {
			t.Fatal("maintenance request lost reviewed scope")
		}
		return result.Success("brine host archive_maintenance_apply", jobs.Accepted{Status: "accepted", OperationID: receipt.OperationID}), nil
	})
	args := []string{"archive", "maintain", "apply", archiveID, "--plan", plan, "--idempotency-key", key, "--target", "fixture", "--json"}
	if noWait {
		args = append(args, "--no-wait")
	}
	if err := Execute(deps, args); err != nil || calls != 1 || polls != map[bool]int{false: 1, true: 0}[noWait] {
		t.Fatal("typed detached archive workflow failed", err, calls, polls)
	}
}
func TestPublicArchiveCLIStatusRequiresTargetAndCannotRunHostCommand(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	called := false
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { called = true; return transport.Target{}, nil }
	if err := Execute(deps, []string{"archive", "status", strings.Repeat("a", 32), "--json"}); err == nil || called {
		t.Fatal("archive status became local host passthrough")
	}
	if err := Execute(deps, []string{"archive", "maintain", "plan", strings.Repeat("a", 32), "--kind", "shell", "--target", "fixture"}); err == nil || called {
		t.Fatal("untyped host action reached target")
	}
}
func TestPublicArchivedCredentialCLIRejectsUnboundedOrOpenPacketBeforeTarget(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.Stdin = strings.NewReader(`{"access_key_id":"PLANTED_ACCESS","secret_access_key":"PLANTED_SECRET","activate":true}`)
	called := false
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { called = true; return transport.Target{}, nil }
	args := []string{"archive", "credentials", "set", strings.Repeat("a", 32), "--binding", strings.Repeat("b", 32), "--epoch", strings.Repeat("c", 32), "--plan", "sha256:" + strings.Repeat("d", 64), "--target", "fixture", "--json"}
	if err := Execute(deps, args); err == nil || called {
		t.Fatal("open renewal packet reached provider or target")
	}
	if strings.Contains(out.String()+diagnostics.String(), "PLANTED") {
		t.Fatal("archive credential leaked into CLI output")
	}
}
