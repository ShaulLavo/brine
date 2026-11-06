//go:build linux

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

func (fx *prepFixture) prepare(host *prepHost, journal sqliteprep.Journal, id, supersedes string) (sqliteprep.Task, sqliteprep.Result, error) {
	task, in := fx.preflight(sqliteprep.PrepareTask, id, supersedes)
	result, err := fx.service(host, journal).Prepare(context.Background(), task, in)
	return task, result, err
}

// prepared runs a fresh LTX preparation to atomic completion.
func (fx *prepFixture) prepared(id string) (sqliteprep.Task, sqliteprep.Receipt) {
	fx.t.Helper()
	task, result, err := fx.prepare(newPrepHost(fx, true), nil, id, "")
	if err != nil {
		fx.t.Fatal(err)
	}
	return task, result.Receipt
}

func TestSQLitePrepareFreshLTXCommitsJoinedAtomicCompletion(t *testing.T) {
	fx := newPrepFixture(t)
	ctx := context.Background()
	host := newPrepHost(fx, true)
	task, result, err := fx.prepare(host, nil, prepID("1"), "")
	if err != nil || result.Receipt.Outcome != sqliteprep.PreparedOutcome || result.CleanupError != nil {
		t.Fatalf("prepare: %+v %v", result, err)
	}
	if fx.state(task.OperationID) != ops.Succeeded || !host.called("barrier") || !host.called("stop_replica") || host.called("capture") || host.called("upload") {
		t.Fatalf("unexpected effects: %v", host.calls)
	}
	p := fx.preparation(prepID("1"))
	if p.point == nil || p.intent.Mode != sqliteprep.Fresh || p.intent.Arm != sqliteprep.LTX || p.intent.Fence.OperationID != task.OperationID || p.events[len(p.events)-1].Stage != sqliteprep.Prepared {
		t.Fatalf("wrong journal: %+v", p.intent)
	}
	outcome, err := fx.s.ReadSQLiteTaskOutcome(ctx, task.OperationID)
	if err != nil || outcome == nil {
		t.Fatalf("joined readback: %v", err)
	}
	status, err := fx.s.SQLitePreparationJournal(fx.observeLocation).ReadTask(ctx, task)
	if err != nil || status.State != sqliteprep.Succeeded || !status.Claimed || status.Receipt == nil || *status.Receipt != result.Receipt {
		t.Fatalf("task read: %+v %v", status, err)
	}
	if n := fx.count("SELECT count(*) FROM data_restore_points WHERE id=?", p.point.Source.Point.ID); n != 1 {
		t.Fatalf("source point rows: %d", n)
	}
	assertSQLiteFenceProtected(t, fx, p.intent.Fence)
}

func assertSQLiteFenceProtected(t *testing.T, fx *prepFixture, f data.QuiescenceFence) {
	t.Helper()
	ctx := context.Background()
	if err := fx.s.ReleaseDataFence(ctx, f.ID, f.OperationID); !errors.Is(err, ErrConflict) {
		t.Fatalf("generic release: %v", err)
	}
	if _, err := fx.s.db.ExecContext(ctx, "UPDATE data_fences SET state='released' WHERE id=?", f.ID); err == nil {
		t.Fatal("raw release bypassed the preparation trigger")
	}
	if fx.count("SELECT count(*) FROM data_fences WHERE id=? AND state='held' AND operation_id=?", f.ID, f.OperationID) != 1 {
		t.Fatal("original fence owner changed")
	}
}

func TestSQLitePreparationRecordsAreImmutable(t *testing.T) {
	fx := newPrepFixture(t)
	fx.prepared(prepID("1"))
	ctx := context.Background()
	for _, statement := range []string{
		"UPDATE data_sqlite_preparations SET mode='supersede_live'",
		"DELETE FROM data_sqlite_preparations",
		"UPDATE data_sqlite_preparation_events SET stage='verified'",
		"DELETE FROM data_sqlite_preparation_events",
		"UPDATE data_sqlite_prepared_points SET source_point_id=source_point_id",
		"DELETE FROM data_sqlite_prepared_points",
		"UPDATE data_sqlite_task_inputs SET app='other'",
		"UPDATE data_fences SET operation_id='other'",
	} {
		if _, err := fx.s.db.ExecContext(ctx, statement); err == nil {
			t.Fatalf("mutated protected record: %s", statement)
		}
	}
	if _, err := fx.s.db.ExecContext(ctx, "INSERT INTO data_sqlite_preparation_events SELECT preparation_id,seq+1,'verified',canonical FROM data_sqlite_preparation_events ORDER BY seq DESC LIMIT 1"); err == nil {
		t.Fatal("event appended after prepared")
	}
}

func TestSQLitePrepareSupersedeReverifyKeepsOriginSource(t *testing.T) {
	fx := newPrepFixture(t)
	_, first := fx.prepared(prepID("1"))
	origin := fx.preparation(prepID("1"))
	host := newPrepHost(fx, false)
	host.writers = true
	task, result, err := fx.prepare(host, nil, prepID("2"), prepID("1"))
	if err != nil {
		t.Fatal(err)
	}
	q := fx.preparation(prepID("2"))
	if q.intent.Mode != sqliteprep.SupersedeReverify || q.intent.CoverageOrigin != prepID("1") || q.intent.BaselineFrom != prepID("1") || q.intent.Fence != origin.intent.Fence {
		t.Fatalf("wrong successor intent: %+v", q.intent)
	}
	for _, effect := range []string{"barrier", "capture", "upload", "stop_writers", "stop_replica"} {
		if host.called(effect) {
			t.Fatalf("reverify issued %s: %v", effect, host.calls)
		}
	}
	if !reflect.DeepEqual(q.point.Source, origin.point.Source) || result.Receipt.SourcePointID != first.SourcePointID || !q.point.Verification.ObservedAt.After(origin.point.Verification.ObservedAt) {
		t.Fatal("reverify moved the origin source or verification age")
	}
	if !fx.preparation(prepID("1")).consumed || fx.state(task.OperationID) != ops.Succeeded {
		t.Fatal("predecessor not atomically consumed")
	}
	if fx.count("SELECT count(*) FROM data_restore_points WHERE id=?", first.SourcePointID) != 1 {
		t.Fatal("origin restore point duplicated")
	}
	// A further successor still resolves the original coverage origin.
	if _, _, err = fx.prepare(newPrepHostStopped(fx), nil, prepID("3"), prepID("2")); err != nil {
		t.Fatal(err)
	}
	r := fx.preparation(prepID("3"))
	if r.intent.CoverageOrigin != prepID("1") || r.point == nil || !reflect.DeepEqual(r.point.Source, origin.point.Source) {
		t.Fatalf("chain moved origin: %+v", r.intent)
	}
	assertSQLiteFenceProtected(t, fx, origin.intent.Fence)
}

func newPrepHostStopped(fx *prepFixture) *prepHost {
	host := newPrepHost(fx, false)
	host.writers = true
	return host
}
