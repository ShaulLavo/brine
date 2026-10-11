package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
)

func TestArchiveMaintenanceRejectsInvalidUsageBeforeRuntime(t *testing.T) {
	id, hash := strings.Repeat("a", 32), "sha256:"+strings.Repeat("b", 64)
	for _, args := range [][]string{
		{"host", "archive-maintain", "/private/data", "--plan", "--kind", "purge_archive"},
		{"host", "archive-maintain", id},
		{"host", "archive-maintain", id, "--plan", "--kind", "unknown"},
		{"host", "archive-maintain", id, "--plan", "--kind", "purge_archive", "--plan-id", hash},
		{"host", "archive-maintain", id, "--plan-id", hash, "--kind", "purge_archive"},
		{"host", "archive-status", "hello"},
		{"host", "archive-purge-reconcile", id, "--path", "/private/data"},
		{"host", "archive-credentials", id, id, "wrong", "--plan"},
		{"host", "archive-credentials", id, id, id, "--plan", "--expires-at", "invalid"},
		{"host", "archive-credentials", id, id, id, "--plan", "--expires-at", "2030-01-01T00:00:00+00:00"},
		{"host", "archive-credentials", id, id, id, "--plan-id", hash, "--expires-at", "2030-01-01T00:00:00Z"},
	} {
		var stdout, stderr bytes.Buffer
		deps := testDependencies(t, &stdout, &stderr)
		if err := ExecuteWithRuntime(deps, args, RuntimeLifecycle{Open: func(context.Context, bool) (RuntimeServices, error) {
			t.Fatal("invalid usage opened runtime", args)
			return RuntimeServices{}, nil
		}}); err == nil {
			t.Fatal("invalid usage admitted", args)
		}
	}
}

type maintenanceCommands struct {
	calls  int
	id     data.ArchiveID
	kind   archive.MaintenanceKind
	planID string
}

func (f *maintenanceCommands) Plan(_ context.Context, id data.ArchiveID, kind archive.MaintenanceKind) (archive.MaintenancePlan, error) {
	f.calls++
	f.id = id
	f.kind = kind
	return archive.MaintenancePlan{ArchiveID: id, Kind: kind}, nil
}
func (f *maintenanceCommands) Apply(_ context.Context, id data.ArchiveID, planID string) (archive.MaintenanceOutcome, error) {
	f.calls++
	f.id = id
	f.planID = planID
	return archive.MaintenanceOutcome{Kind: archive.PurgeArchive}, nil
}
func (f *maintenanceCommands) ReconcileDelete(_ context.Context, id data.ArchiveID) (archive.Deletion, error) {
	f.calls++
	f.id = id
	return archive.Deletion{ArchiveID: id}, nil
}
func (f *maintenanceCommands) Status(_ context.Context, id data.ArchiveID) (archive.MaintenanceStatus, error) {
	f.calls++
	f.id = id
	return archive.MaintenanceStatus{ArchiveID: id, State: "degraded"}, nil
}

func TestArchiveMaintenanceMachineRoutingUsesExplicitArchiveID(t *testing.T) {
	id, hash := strings.Repeat("a", 32), "sha256:"+strings.Repeat("b", 64)
	for _, args := range [][]string{
		{"host", "archive-maintain", id, "--plan", "--kind", "verify_retention"},
		{"host", "archive-maintain", id, "--plan", "--kind", "purge_archive"},
		{"host", "archive-maintain", id, "--plan", "--kind", "expire_archive"},
		{"host", "archive-maintain", id, "--plan-id", hash},
		{"host", "archive-status", id},
		{"host", "archive-purge-reconcile", id},
	} {
		var stdout, stderr bytes.Buffer
		deps := testDependencies(t, &stdout, &stderr)
		f := &maintenanceCommands{}
		deps.HostArchiveMaintenance = f
		root := NewRootCommand(deps)
		root.SetArgs(args)
		if err := root.ExecuteContext(context.Background()); err != nil || f.calls != 1 || string(f.id) != id {
			t.Fatal("incorrect maintenance routing", args, f, err)
		}
		if !HostArchiveRequested(context.Background(), args) || !HostRuntimeRequested(args) {
			t.Fatal("missing runtime process protections", args)
		}
		var value map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &value); err != nil || value["ok"] != true {
			t.Fatal("invalid machine result", stdout.String(), err)
		}
		if len(args) > 4 && args[3] == "--plan" && string(f.kind) != args[5] {
			t.Fatal("incorrect kind", f.kind, args)
		}
		if args[1] == "archive-maintain" && args[3] == "--plan-id" && f.planID != hash {
			t.Fatal("lost reviewed plan ID")
		}
	}
}

type archiveCredentialCommands struct {
	planCalls, deliverCalls int
	ids                     []string
	expiry                  *time.Time
}

func (f *archiveCredentialCommands) Plan(_ context.Context, a, b, e string, expiry *time.Time) (backupcredentials.ArchivePlan, error) {
	f.planCalls++
	f.ids = []string{a, b, e}
	f.expiry = expiry
	return backupcredentials.ArchivePlan{Scope: backupcredentials.ArchiveScope{ArchiveID: a, Binding: b, Epoch: e}}, nil
}
func (f *archiveCredentialCommands) DeliverScoped(_ context.Context, a, b, e, id string, packet backupcredentials.Packet) (backupcredentials.ArchiveReceipt, error) {
	f.deliverCalls++
	f.ids = []string{a, b, e, id}
	if len(packet.Environment()) != 2 {
		return backupcredentials.ArchiveReceipt{}, backupcredentials.ErrInvalid
	}
	return backupcredentials.ArchiveReceipt{Plan: backupcredentials.ArchivePlan{Scope: backupcredentials.ArchiveScope{ArchiveID: a, Binding: b, Epoch: e}}}, nil
}
func TestArchiveCredentialMachineDeliveryIsPrivateAndScoped(t *testing.T) {
	a, b, e, hash := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), "sha256:"+strings.Repeat("d", 64)
	for _, planned := range []bool{true, false} {
		var stdout, stderr bytes.Buffer
		deps := testDependencies(t, &stdout, &stderr)
		deps.Stdin = strings.NewReader(`{"access_key_id":"PLANTED_ACCESS","secret_access_key":"PLANTED_SECRET"}`)
		f := &archiveCredentialCommands{}
		deps.HostArchiveCredentials = f
		args := []string{"host", "archive-credentials", a, b, e}
		if planned {
			args = append(args, "--plan", "--expires-at", "2030-01-01T00:00:00Z")
		} else {
			args = append(args, "--plan-id", hash)
		}
		root := NewRootCommand(deps)
		root.SetArgs(args)
		if err := root.ExecuteContext(context.Background()); err != nil || f.planCalls+f.deliverCalls != 1 || (f.planCalls == 1) != planned {
			t.Fatal("incorrect credential routing", f, err)
		}
		if f.ids[0] != a || f.ids[1] != b || f.ids[2] != e || !planned && f.ids[3] != hash {
			t.Fatal("credential scope lost", f.ids)
		}
		if planned && f.expiry == nil {
			t.Fatal("expiry not bound")
		}
		if strings.Contains(stdout.String()+stderr.String(), "PLANTED_") {
			t.Fatal("credential value leaked")
		}
		if !HostArchiveRequested(context.Background(), args) || !HostRuntimeRequested(args) {
			t.Fatal("missing credential runtime safeguards")
		}
	}
}
