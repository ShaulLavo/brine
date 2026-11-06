//go:build linux

package host

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/sqlitestatus"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestReadinessConnectedSQLiteReusesSnapshotAndFixtures(t *testing.T) {
	_, reader, _, dir := sqliteStatusRig(t, nil, true)
	scratch := t.TempDir()
	reader.open = store.OpenSnapshotReadOnly
	reader.workspaceRoot = scratch
	reader.probe = func(context.Context, restore.Destination, restore.Credentials) restore.RemoteProbe {
		return restore.RemoteProbe{State: restore.RemoteReachable, ObservedAt: time.Now().UTC()}
	}
	before := diagnosticFiles(t, dir)
	status, err := reader.SQLiteBackupStatus(t.Context(), dispatch.SQLiteBackupStatusArgs{App: "example", Database: "main"})
	if err != nil || status.Watermark.State != "known" || status.LastVerified != nil || status.Loss != "unknown" || status.LocalPosition != nil {
		t.Fatal(status, err)
	}
	if !reflect.DeepEqual(before, diagnosticFiles(t, dir)) {
		t.Fatal("connected status changed live control files or locks")
	}
	if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
		t.Fatal("owned status scratch retained", entries, err)
	}
	stale := sqliteReadinessStale(status)
	if stale.Value == nil || !*stale.Value {
		t.Fatal("missing independently verified point became readiness", stale)
	}
}
func TestReadinessSQLiteUnknownDoesNotFailBackupRule(t *testing.T) {
	status := sqlitestatus.Status{Committed: true, LastVerified: &sqlitestatus.Verified{}, Credential: sqlitestatus.Credential{State: "available"}, Lifecycle: sqlitestatus.Lifecycle{State: "fresh"}, Service: sqlitestatus.Service{State: "active"}, Remote: restore.RemoteProbe{State: restore.RemoteNotChecked}, Watermark: sqlitestatus.Watermark{State: "not_checked"}}
	stale := sqliteReadinessStale(status)
	if stale.Status != "unknown" || stale.Value != nil {
		t.Fatal(stale)
	}
	status.Remote.State = restore.RemoteReachable
	status.Watermark.State = "known"
	stale = sqliteReadinessStale(status)
	if stale.Value == nil || *stale.Value {
		t.Fatal(stale)
	}
	status.Lifecycle.State = "stale"
	if stale = sqliteReadinessStale(status); stale.Value == nil || !*stale.Value {
		t.Fatal(stale)
	}
}

func TestReadinessSQLiteKnownDegradationControls(t *testing.T) {
	for _, kind := range []string{"uncommitted", "renewal", "fenced"} {
		testSQLiteKnownDegradation(t, kind)
	}
}
func testSQLiteKnownDegradation(t *testing.T, kind string) {
	t.Helper()
	status := sqlitestatus.Status{Committed: true, LastVerified: &sqlitestatus.Verified{}, Credential: sqlitestatus.Credential{State: "available"}, Lifecycle: sqlitestatus.Lifecycle{State: "fresh"}, Service: sqlitestatus.Service{State: "active"}, Remote: restore.RemoteProbe{State: restore.RemoteReachable}, Watermark: sqlitestatus.Watermark{State: "known"}}
	switch kind {
	case "uncommitted":
		status.Committed = false
	case "renewal":
		status.Credential.RenewalNeeded = true
	case "fenced":
		status.HeldFences = []sqlitestatus.Fence{{ID: "fixture"}}
	}
	stale := sqliteReadinessStale(status)
	if stale.Value == nil || !*stale.Value {
		t.Fatalf("known %s degradation became health: %+v", kind, stale)
	}
}
