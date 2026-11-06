package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/sqlitestatus"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestSQLiteStatusMachineAndHumanContract(t *testing.T) {
	for _, mode := range []string{"", "--json", "--jsonl"} {
		t.Run(mode, func(t *testing.T) {
			sqliteStatusCLICase(t, mode)
		})
	}
}
func sqliteStatusCLICase(t *testing.T, mode string) {
	t.Helper()
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	calls := 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		calls++
		var a dispatch.SQLiteBackupStatusArgs
		if request.Op != "sqlite_backup_status" || json.Unmarshal(request.Args, &a) != nil || a.App != "hello" || a.Database != "main" {
			t.Fatal("status selector lost")
		}
		now := time.Now().UTC()
		s := sqlitestatus.Compose(now, sqlitestatus.Facts{App: "hello", Database: "main", DatabaseID: strings.Repeat("a", 32), BindingID: strings.Repeat("b", 32), EpochID: strings.Repeat("c", 32), Credential: sqlitestatus.Credential{State: "missing"}}, sqlitestatus.Observations{Service: sqlitestatus.Service{State: "active", ObservedAt: now}, Remote: restore.RemoteProbe{State: restore.RemoteNotChecked, ObservedAt: now}, Watermark: sqlitestatus.Watermark{State: "not_checked", ObservedAt: now}})
		return result.Success("brine host sqlite_backup_status", s), nil
	})
	args := []string{"restore", "status", "hello", "--database", "main", "--target", "fixture"}
	if mode != "" {
		args = append(args, mode)
	}
	if err := Execute(deps, args); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(out.String(), "unknown") || stderr.Len() != 0 {
		t.Fatalf("bad status output %s / %s", out.String(), stderr.String())
	}
	if mode == "" && !strings.Contains(out.String(), "Running is not durability") {
		t.Fatal("human output implies durability")
	}
	if mode != "" && (!strings.Contains(out.String(), `"command":"brine restore status"`) || !strings.Contains(out.String(), `"local_position":null`)) {
		t.Fatal("machine contract lost unknown values")
	}
}

func TestSQLiteStatusRejectsUnsafeSelectionBeforeIO(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
		t.Fatal("invalid status did IO")
		return transport.Target{}, nil
	}
	if err := Execute(deps, []string{"restore", "status", "hello", "--database", "../data", "--target", "fixture"}); err == nil {
		t.Fatal("unsafe selector admitted")
	}
}
