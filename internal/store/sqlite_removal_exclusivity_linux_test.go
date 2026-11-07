//go:build linux

package store

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

func TestSQLiteRemovalMigrationKeepsOrdinaryTaskExclusive(t *testing.T) {
	fx := newPrepFixture(t)
	fx.prepared(prepID("1"))
	successor, _, err := fx.prepare(newPrepHostStopped(fx), nil, prepID("2"), prepID("1"))
	if err != nil {
		t.Fatal(err)
	}
	// Isolate the duplicate-task guard from the independently tested exact-input
	// guard. Every source preparation and the first consumer are genuine.
	execRemovalFixture(t, fx.s, "DROP TRIGGER data_sqlite_point_consumers_identity")
	for _, kind := range []string{"supersede", "abandon", "reviewed_mutation"} {
		var next any
		if kind == "supersede" {
			next = prepID("3")
		}
		_, err = fx.s.db.ExecContext(t.Context(), "INSERT INTO data_sqlite_point_consumers VALUES(?,?,?,?,?)", prepID("2"), successor.OperationID, kind, next, []byte("{}"))
		if err == nil || !strings.Contains(err.Error(), "non-archive task already consumed") {
			t.Fatal("ordinary/cross-kind duplicate-task protection lost", kind, err)
		}
	}
}

func TestSQLiteRemovalMigrationKeepsOrdinaryCompletionExclusive(t *testing.T) {
	fx := newPrepFixture(t)
	fx.prepared(prepID("1"))
	if _, _, err := fx.prepare(newPrepHostStopped(fx), nil, prepID("2"), prepID("1")); err != nil {
		t.Fatal(err)
	}
	task, in := fx.preflight(sqliteprep.AbandonTask, prepID("2"), "")
	result, err := fx.service(newPrepHostStopped(fx), nil).Abandon(t.Context(), task, in)
	if err != nil {
		t.Fatal(err)
	}
	execRemovalFixture(t, fx.s, "DROP TRIGGER data_sqlite_consumer_completions_exact")
	_, err = fx.s.db.ExecContext(t.Context(), "INSERT INTO data_sqlite_consumer_completions VALUES(?,?,?)", prepID("1"), task.OperationID, result.Receipt.Fence.ID)
	if err == nil || !strings.Contains(err.Error(), "non-archive task already completed") {
		t.Fatal("ordinary completion duplicated across preparations", err)
	}
}
