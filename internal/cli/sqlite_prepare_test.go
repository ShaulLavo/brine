package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
	"github.com/ShaulLavo/brine/internal/transport"
)

func sqlitePrepareCLIReceipt(kind sqliteprep.TaskKind) sqliteprep.Receipt {
	task := sqliteprep.Task{OperationID: "sqlite-operation", Kind: kind, Requester: "deploy:" + strings.Repeat("a", 64), App: "hello", InputSHA256: strings.Repeat("b", 64)}
	fence := data.QuiescenceFence{ID: data.FenceID(strings.Repeat("c", 32)), DatabaseID: data.DatabaseID(strings.Repeat("d", 32)), IncarnationID: data.AppIncarnationID(strings.Repeat("e", 32)), OperationID: "sqlite-origin", State: data.FenceHeld}
	r := sqliteprep.Receipt{Task: task, PreparationID: strings.Repeat("f", 32), PreparedSHA256: strings.Repeat("1", 64), Fence: fence, SourcePointID: strings.Repeat("f", 32), Outcome: sqliteprep.PreparedOutcome, CompletedAt: time.Now().UTC()}
	if kind == sqliteprep.AbandonTask {
		r.Outcome, r.OldObject, r.Fence.State = sqliteprep.AbandonedOutcome, sqliteprep.ObjectUnknown, data.FenceReleased
	}
	return r
}

func TestSQLitePrepareAndAbandonWaitForExactReceipt(t *testing.T) {
	for _, kind := range []sqliteprep.TaskKind{sqliteprep.PrepareTask, sqliteprep.AbandonTask} {
		t.Run(string(kind), func(t *testing.T) { sqlitePrepareCLICase(t, kind) })
	}
}

func sqlitePrepareCLICase(t *testing.T, kind sqliteprep.TaskKind) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	receipt := sqlitePrepareCLIReceipt(kind)
	raw, err := sqliteprep.Encode(receipt)
	if err != nil {
		t.Fatal(err)
	}
	submitted := 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		if request.Op == "operation" {
			op := ops.Operation{ID: "sqlite-operation", Kind: ops.Kind(kind), App: "hello", Requester: receipt.Task.Requester, SecretRef: receipt.Task.InputSHA256, State: ops.Succeeded}
			return result.Success("brine host operation", jobs.Status{Operation: op, Events: []ops.Event{}, Outcome: &ops.TaskOutcome{Receipt: raw}}), nil
		}
		submitted++
		checkSQLitePrepareRequest(t, kind, request)
		return result.Success("brine host "+string(kind), jobs.Accepted{Status: "accepted", OperationID: "sqlite-operation"}), nil
	})
	args := []string{"restore", "prepare", "hello", "--database", "main", "--supersedes", strings.Repeat("9", 32), "--target", "fixture", "--idempotency-key", strings.Repeat("a", 32)}
	if kind == sqliteprep.AbandonTask {
		args = []string{"restore", "abandon", strings.Repeat("f", 32), "--target", "fixture", "--idempotency-key", strings.Repeat("a", 32)}
	}
	if err = Execute(deps, args); err != nil || submitted != 1 {
		t.Fatal("command did not wait on its accepted task", err, submitted)
	}
	expected := "fence " + strings.Repeat("c", 32) + " remains held"
	if kind == sqliteprep.AbandonTask {
		expected = "Nothing was started"
	}
	if !strings.Contains(out.String(), expected) {
		t.Fatalf("human output %q", out.String())
	}
}

func checkSQLitePrepareRequest(t *testing.T, kind sqliteprep.TaskKind, request dispatch.Request) {
	t.Helper()
	if request.Op != string(kind) {
		t.Fatal("wrong operation", request.Op)
	}
	if kind == sqliteprep.AbandonTask {
		var a dispatch.SQLiteAbandonArgs
		if json.Unmarshal(request.Args, &a) != nil || a.PreparationID != strings.Repeat("f", 32) || a.IdempotencyKey != strings.Repeat("a", 32) {
			t.Fatal("abandon selector lost")
		}
		return
	}
	var a dispatch.SQLitePrepareArgs
	if json.Unmarshal(request.Args, &a) != nil || a.App != "hello" || a.Database != "main" || a.Supersedes != strings.Repeat("9", 32) || a.IdempotencyKey != strings.Repeat("a", 32) {
		t.Fatal("prepare selector lost")
	}
}

func TestSQLitePrepareHelpSaysItStopsWriters(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	if err := Execute(deps, []string{"restore", "prepare", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"STOPS the app's writers", "including across a reboot", "never started or restarted"} {
		if !strings.Contains(out.String(), phrase) {
			t.Fatalf("help lacks %q", phrase)
		}
	}
}
