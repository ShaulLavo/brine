package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/sqlitestatus"
)

type sqliteStatusBudgetRunner struct {
	calls                        int
	clientDeadline, hostDeadline time.Time
	wait                         bool
}

func (r *sqliteStatusBudgetRunner) RunInput(ctx context.Context, c localexec.Command) (localexec.Output, error) {
	r.calls++
	r.clientDeadline, _ = ctx.Deadline()
	server := dispatch.NewServer("fixture", nil)
	server.SQLiteBackupStatus = r
	response, err := server.Handle(ctx, bytes.NewReader(c.Stdin))
	raw, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		return localexec.Output{}, marshalErr
	}
	return localexec.Output{Stdout: raw}, err
}
func (r *sqliteStatusBudgetRunner) SQLiteBackupStatus(ctx context.Context, _ dispatch.SQLiteBackupStatusArgs) (sqlitestatus.Status, error) {
	r.hostDeadline, _ = ctx.Deadline()
	if r.wait {
		<-ctx.Done()
		return sqlitestatus.Status{}, ctx.Err()
	}
	now := time.Now().UTC()
	return sqlitestatus.Compose(now, sqlitestatus.Facts{App: "example", Database: "main", DatabaseID: strings.Repeat("a", 32), BindingID: strings.Repeat("b", 32), EpochID: strings.Repeat("c", 32), Credential: sqlitestatus.Credential{State: "missing"}}, sqlitestatus.Observations{Service: sqlitestatus.Service{State: "unknown", ObservedAt: now}, Remote: restore.RemoteProbe{State: restore.RemoteNotChecked, ObservedAt: now}, Watermark: sqlitestatus.Watermark{State: "not_checked", ObservedAt: now}}), nil
}
func TestSQLiteStatusBudgetOnActualClientAndDispatch(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		sqliteStatusTransportCase(t, cancelParent)
	}
}
func sqliteStatusTransportCase(t *testing.T, cancelParent bool) {
	t.Helper()
	runner := &sqliteStatusBudgetRunner{wait: cancelParent}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil { // #nosec G302 -- Private fixture directory requires traversal.
		t.Fatal(err)
	}
	client := Client{KnownHostsDir: dir, Runner: runner, LookPath: func(string) (string, error) { return "/fixture/ssh", nil }}
	ctx := t.Context()
	cancel := func() {}
	deadline := time.Time{}
	if cancelParent {
		deadline = time.Now().Add(time.Second)
		ctx, cancel = context.WithDeadline(ctx, deadline)
	}
	started := time.Now()
	response, err := client.Call(ctx, validTarget(), dispatch.Request{SchemaVersion: 1, Op: "sqlite_backup_status", RequestID: "budget", Args: json.RawMessage(`{"app":"example","database":"main"}`)})
	cancel()
	if runner.calls != 1 {
		t.Fatal("status retried")
	}
	if cancelParent {
		if !errors.Is(err, context.DeadlineExceeded) || response.OK || !runner.clientDeadline.Equal(deadline) || !runner.hostDeadline.Equal(deadline) {
			t.Fatal("extended parent deadline", err)
		}
		return
	}
	if err != nil || !response.OK {
		t.Fatal(err)
	}
	if runner.hostDeadline.Before(started.Add(dispatch.SQLiteBackupStatusTimeout)) || runner.hostDeadline.After(started.Add(dispatch.SQLiteBackupStatusTimeout+time.Second)) {
		t.Fatal("status dispatch uses standard timeout")
	}
	if runner.clientDeadline.Before(started.Add(dispatch.CallTimeout("sqlite_backup_status"))) || runner.clientDeadline.After(started.Add(dispatch.CallTimeout("sqlite_backup_status")+time.Second)) {
		t.Fatal("client omitted shared operation budget")
	}
	if dispatch.OperationTimeout("sqlite_backup_status") != time.Minute || dispatch.CallTimeout("sqlite_backup_status") != dispatch.RequestTimeout+time.Minute+dispatch.ReviewCleanupTimeout+dispatch.SSHConnectTimeout || dispatch.CallTimeout("native_backup_status") != dispatch.RequestTimeout {
		t.Fatal("unrelated operation deadline changed")
	}
}
