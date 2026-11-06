package ops

import (
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

func sqliteTaskOperation(kind Kind) Operation {
	return Operation{ID: "sqlite-op", Kind: kind, App: "hello", SecretRef: strings.Repeat("a", 64), Requester: "deploy:" + strings.Repeat("b", 64), IdempotencyKey: "key", State: Succeeded}
}

func sqliteAbandonReceipt(op Operation) []byte {
	id := strings.Repeat("c", 32)
	r := sqliteprep.Receipt{OldObject: sqliteprep.ObjectUnknown, Task: sqliteprep.Task{OperationID: op.ID, Kind: sqliteprep.TaskKind(op.Kind), Requester: op.Requester, App: op.App, InputSHA256: op.SecretRef}, PreparationID: id, Fence: data.QuiescenceFence{ID: data.FenceID(id), IncarnationID: data.AppIncarnationID(id), DatabaseID: data.DatabaseID(id), OperationID: "owner", State: data.FenceReleased}, Outcome: sqliteprep.AbandonedOutcome, CompletedAt: time.Now().UTC()}
	raw, err := sqliteprep.Encode(r)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestSQLitePreparationTaskShapes(t *testing.T) {
	for _, kind := range []Kind{SQLitePrepare, SQLitePrepareAbandon} {
		valid := Intent{Kind: kind, App: "hello", SecretRef: strings.Repeat("a", 64)}
		if !kind.IsTask() || kind.IsResolver() || !ValidIntent(valid) || !CanTransitionFor(kind, Preflight, Failed) || CanTransitionFor(kind, Preflight, Preparing) {
			t.Fatalf("%s shape", kind)
		}
		refuseSQLiteIntents(t, []Intent{{Kind: kind, App: "hello", SecretRef: "short"}, {Kind: kind, App: "hello", SecretRef: strings.Repeat("A", 64)}, {Kind: kind, App: "hello", SecretRef: strings.Repeat("a", 64), PlanID: "sha256:" + strings.Repeat("a", 64)}, {Kind: kind, SecretRef: strings.Repeat("a", 64)}, {Kind: kind, App: "hello", SecretRef: strings.Repeat("a", 64), RecoveryOf: "x"}})
	}
}

func refuseSQLiteIntents(t *testing.T, intents []Intent) {
	t.Helper()
	for _, invalid := range intents {
		if ValidIntent(invalid) {
			t.Fatalf("accepted %+v", invalid)
		}
	}
}

func TestSQLitePreparationReceiptBindsExactTask(t *testing.T) {
	op := sqliteTaskOperation(SQLitePrepareAbandon)
	raw := sqliteAbandonReceipt(op)
	if !(TaskOutcome{Receipt: raw}).Valid(op) {
		t.Fatal("exact receipt refused")
	}
	for name, crossed := range map[string]func(*Operation){
		"operation": func(o *Operation) { o.ID = "other" },
		"requester": func(o *Operation) { o.Requester = "deploy:" + strings.Repeat("d", 64) },
		"app":       func(o *Operation) { o.App = "other" },
		"input":     func(o *Operation) { o.SecretRef = strings.Repeat("e", 64) },
		"kind":      func(o *Operation) { o.Kind = SQLitePrepare },
	} {
		other := op
		crossed(&other)
		if (TaskOutcome{Receipt: raw}).Valid(other) {
			t.Fatalf("receipt accepted crossed %s", name)
		}
	}
	if (TaskOutcome{Receipt: append(raw[:len(raw):len(raw)], ' ')}).Valid(op) {
		t.Fatal("noncanonical receipt accepted")
	}
}
