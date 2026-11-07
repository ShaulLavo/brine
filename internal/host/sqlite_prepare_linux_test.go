//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

// A stopped replica and a running writer: fresh snapshot preparation through
// the real factory composition pieces, runner, store and files. The writer is
// stopped, nothing is started, a snapshot is created once with its 412 check,
// verified in isolation, and the selected-point restore test admits it while
// its fence is held. Abandon then releases only that fence and starts nothing.
func TestSQLitePrepareSnapshotTestAndAbandonThroughRealRunner(t *testing.T) {
	r := newSQLitePrepRig(t, []string{"v1", "v2"}, "v2")
	op, receipt := r.prepare(context.Background(), "")
	if receipt == nil || receipt.Outcome != sqliteprep.PreparedOutcome {
		t.Fatalf("preparation %s did not succeed", op.State)
	}
	puts, gets := r.remote.counts()
	if puts != 2 || gets != 1 || r.exec.count("systemctl --user stop hello.service") != 1 || r.exec.count("stop "+r.replicaUnit()) != 0 {
		t.Fatalf("effects: puts=%d gets=%d calls=%q", puts, gets, r.exec.calls)
	}
	r.exec.assertNothingStarted(t)
	if r.heldFences() != 1 || r.replicaPermit() == nil || r.writerPermit() == nil {
		t.Fatal("held preparation fence does not block replica and writer startup permits")
	}
	source, err := r.state.ReadSQLitePreparedSource(context.Background(), "hello", "main", receipt.PreparationID)
	if err != nil || source.Point.Kind != data.RestorePointSnapshot || source.Point.Schema.Marker != "v2" || source.Point.ID != receipt.SourcePointID {
		t.Fatalf("multi-marker source not resolved by observation: %+v %v", source.Point, err)
	}
	tests := restoreTests{stateDir: r.root, cli: r.host().cli, endpoint: r.remote.server.URL, allowHTTP: true}
	verified, err := tests.Check(context.Background(), dispatch.RestoreTestArgs{App: "hello", Database: "main", Point: receipt.PreparationID}, strings.Repeat("9", 32))
	if err != nil || !verified.Valid() || verified.Source.Snapshot == nil || verified.Source.Snapshot.PointID != receipt.SourcePointID || verified.LossWindow.State != restore.LossBounded || verified.Schema.Marker != "v2" {
		t.Fatalf("selected prepared point not testable under its held fence: %+v %v", verified, err)
	}
	abandoned, abandon := r.abandon(receipt.PreparationID)
	if abandon == nil || abandon.Outcome != sqliteprep.AbandonedOutcome || abandon.Fence.ID != receipt.Fence.ID || abandon.Fence.State != data.FenceReleased {
		t.Fatalf("abandon %s", abandoned.State)
	}
	r.exec.assertNothingStarted(t)
	if r.heldFences() != 0 || r.replicaPermit() != nil || r.writerPermit() != nil {
		t.Fatal("released fence does not restore the existing boot/start permits", r.replicaPermit(), r.writerPermit())
	}
	if units := r.exec.units; units["hello.service"] != "stopped" || units[r.replicaUnit()] != "stopped" {
		t.Fatal("abandon changed unit state")
	}
}

// A running replica proven to own its lifetime lock: barrier through its own
// socket, verify the exact TXID, stop it and hold the stopped source through
// completion. A supersede then re-verifies the retained origin without any
// barrier, stop or upload, and the successor's point test resolves to the
// origin's source point.
func TestSQLitePrepareLiveLTXThenReverifySuccessor(t *testing.T) {
	r := newSQLitePrepRig(t, []string{"v2"}, "v2")
	r.startSupervisor()
	_, first := r.prepare(context.Background(), "")
	if first == nil || r.exec.count("litestream sync") != 1 || r.exec.count("stop "+r.replicaUnit()) != 1 || r.exec.count("stop hello.service") != 1 {
		t.Fatalf("live LTX preparation: %+v calls=%q", first, r.exec.calls)
	}
	syncs, puts := r.exec.count("litestream sync"), r.remote.puts
	_, second := r.prepare(context.Background(), first.PreparationID)
	if second == nil || second.SourcePointID != first.SourcePointID || second.Fence != first.Fence {
		t.Fatalf("reverify successor did not retain the origin source and fence: %+v", second)
	}
	if r.exec.count("litestream sync") != syncs || r.remote.puts != puts || r.exec.count("stop "+r.replicaUnit()) != 1 || r.exec.count("stop hello.service") != 1 {
		t.Fatalf("reverify repeated a barrier, upload or stop: %q", r.exec.calls)
	}
	r.exec.assertNothingStarted(t)
	source, err := r.state.ReadSQLitePreparedSource(context.Background(), "hello", "", second.PreparationID)
	if err != nil || source.Point.ID != first.PreparationID || source.Point.LTX == nil || source.Point.LTX.TXID != 7 {
		t.Fatalf("successor did not resolve to the origin point: %+v %v", source.Point, err)
	}
	tests := restoreTests{stateDir: r.root, cli: r.host().cli, endpoint: r.remote.server.URL, allowHTTP: true}
	verified, err := tests.Check(context.Background(), dispatch.RestoreTestArgs{App: "hello", Point: second.PreparationID}, strings.Repeat("8", 32))
	if err != nil || verified.Source.LTX == nil || verified.RecoveredTXID != 7 {
		t.Fatalf("successor point test: %+v %v", verified, err)
	}
	if _, err = tests.Check(context.Background(), dispatch.RestoreTestArgs{App: "other", Point: second.PreparationID}, strings.Repeat("7", 32)); err == nil {
		t.Fatal("a foreign app's point test fell back or crossed preparations")
	}
	if _, abandon := r.abandon(first.PreparationID); abandon != nil {
		t.Fatal("a consumed predecessor was abandoned")
	}
	if _, abandon := r.abandon(second.PreparationID); abandon == nil || r.heldFences() != 0 {
		t.Fatal("successor abandon did not release the original fence")
	}
	r.exec.assertNothingStarted(t)
}

// The replica unit is running but its lock holder is not the unit's own main
// process in its cgroup. Ownership is unproved, so nothing is claimed or stopped.
func TestSQLitePrepareRefusesUnprovedReplicaOwnership(t *testing.T) {
	for name, cgroup := range map[string]string{"foreign cgroup": "0::/user.slice/other.service\n", "missing cgroup": ""} {
		t.Run(name, func(t *testing.T) { checkUnprovedReplicaOwner(t, cgroup) })
	}
}

func checkUnprovedReplicaOwner(t *testing.T, cgroup string) {
	r := newSQLitePrepRig(t, []string{"v2"}, "v2")
	r.startSupervisor()
	r.writeProc("4242/cgroup", cgroup)
	op, receipt := r.prepare(context.Background(), "")
	if receipt != nil || op.State != ops.Failed || r.exec.count(" stop ") != 0 || r.heldFences() != 0 {
		t.Fatalf("unproved owner admitted: %s %q", op.State, r.exec.calls)
	}
}

// The writer is stopped and the source captured; then a WAL file is replaced
// with a new inode while the main database inode and catalog are unchanged.
// Completion refuses, the task stays recovery-required with its fence held,
// and abandon refuses to endorse the changed source. Nothing starts.
func TestSQLitePrepareReplacedWALRefusesCompletionAndAbandon(t *testing.T) {
	r := newSQLitePrepRig(t, []string{"v2"}, "v2")
	if err := os.WriteFile(r.path("-wal"), []byte("inert wal bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	r.remote.onPut = func(*http.Request) {
		replacement := r.path("-wal.new")
		if err := os.WriteFile(replacement, []byte("inert wal bytes"), 0600); err != nil {
			t.Error(err)
		}
		if err := os.Rename(replacement, r.path("-wal")); err != nil {
			t.Error(err)
		}
	}
	op, receipt := r.prepare(context.Background(), "")
	if receipt != nil || op.State != ops.RecoveryRequired || r.heldFences() != 1 {
		t.Fatalf("changed stopped source completed: %s", op.State)
	}
	r.remote.onPut = nil
	preparation := r.preparationID(op)
	abandoned, abandon := r.abandon(preparation)
	if abandon != nil || abandoned.State != ops.Failed || r.heldFences() != 1 {
		t.Fatalf("abandon endorsed a changed source: %s", abandoned.State)
	}
	r.exec.assertNothingStarted(t)
}

// SQLite's SHM index may be rebuilt; its absent-to-present transition alone
// is not an application data change.
func TestSQLitePrepareToleratesSHMRebuild(t *testing.T) {
	r := newSQLitePrepRig(t, []string{"v2"}, "v2")
	r.remote.onPut = func(*http.Request) {
		if err := os.WriteFile(r.path("-shm"), make([]byte, 32768), 0600); err != nil {
			t.Error(err)
		}
	}
	if _, receipt := r.prepare(context.Background(), ""); receipt == nil {
		t.Fatal("SHM rebuild refused")
	}
}

// Cancellation during the single upload invocation leaves an uncertain claimed
// task fenced and recovery-required. Abandon then releases the fence with the
// remote object unknown, starting nothing.
func TestSQLitePrepareCancelledUploadThenAbandon(t *testing.T) {
	r := newSQLitePrepRig(t, []string{"v2"}, "v2")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.remote.onPut = func(req *http.Request) {
		cancel()
		// Drain the body so the server notices the client's disconnect.
		_, _ = io.Copy(io.Discard, req.Body)
		select {
		case <-req.Context().Done():
		case <-time.After(10 * time.Second):
			t.Error("cancelled upload request stayed connected")
		}
	}
	op, receipt := r.prepare(ctx, "")
	if receipt != nil || op.State != ops.RecoveryRequired || r.heldFences() != 1 {
		t.Fatalf("cancelled upload: %s", op.State)
	}
	r.remote.onPut = nil
	_, abandon := r.abandon(r.preparationID(op))
	if abandon == nil || abandon.OldObject != sqliteprep.ObjectUnknown || r.heldFences() != 0 {
		t.Fatalf("uncertain preparation not abandonable: %+v", abandon)
	}
	r.exec.assertNothingStarted(t)
}

// The handler's reply is lost after the atomic completion committed. The runner
// reads back the joined success. A retried admission with the same key names
// the same operation, and running it again repeats no host or remote effect.
func TestSQLitePrepareLostAcknowledgementAndSameKeyRetry(t *testing.T) {
	r := newSQLitePrepRig(t, []string{"v2"}, "v2")
	o := r.operations()
	args := dispatch.SQLitePrepareArgs{App: "hello", Database: "main", IdempotencyKey: r.key()}
	accepted, err := o.Prepare(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	runner := r.runner(o)
	inner := runner.TaskHandlers[ops.SQLitePrepare]
	runner.TaskHandlers[ops.SQLitePrepare] = func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		if _, err := inner(ctx, op); err != nil {
			return nil, err
		}
		return nil, errors.New("acknowledgement lost after commit")
	}
	_ = runner.Run(context.Background(), accepted.OperationID)
	if op, receipt := r.outcome(accepted.OperationID); receipt == nil {
		t.Fatalf("committed success not read back: %s", op.State)
	}
	again, err := o.Prepare(context.Background(), args)
	if err != nil || again.OperationID != accepted.OperationID {
		t.Fatal("same-key retry did not name the same operation", err)
	}
	stops, puts := r.exec.count(" stop "), r.remote.puts
	_ = r.runner(o).Run(context.Background(), accepted.OperationID)
	if _, receipt := r.outcome(accepted.OperationID); receipt == nil || r.exec.count(" stop ") != stops || r.remote.puts != puts {
		t.Fatal("rerun repeated effects or lost the committed outcome")
	}
	r.exec.assertNothingStarted(t)
}

// A marker that no declared, registered definition names is never guessed:
// the task fails before any claim, fence or stop.
func TestSQLitePrepareRefusesUnregisteredObservedMarker(t *testing.T) {
	r := newSQLitePrepRig(t, []string{"v1", "v2"}, "v2")
	writeSQLitePrepDatabase(t, r.path(""), "v3")
	op, receipt := r.prepare(context.Background(), "")
	if receipt != nil || op.State != ops.Failed || r.heldFences() != 0 || r.exec.count(" stop ") != 0 {
		t.Fatalf("unregistered marker admitted: %s %q", op.State, r.exec.calls)
	}
}

// The retained handles see a replaced WAL with the main inode unchanged and a
// replaced directory, ignore SHM, fail after Close, and never let Close run
// while a Recheck is in progress.
func TestSQLiteSourceRetainedHandles(t *testing.T) {
	r := newSQLitePrepRig(t, []string{"v2"}, "v2")
	if err := os.WriteFile(r.path("-wal"), []byte("inert wal bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := openSQLiteSource(r.database, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	baseline, err := source.Recheck(ctx)
	if err != nil || baseline.WAL.Presence != sqliteprep.Regular || baseline.WAL.ProofSHA256 == "" {
		t.Fatal(baseline, err)
	}
	if err = os.WriteFile(r.path("-shm"), []byte("index"), 0600); err != nil {
		t.Fatal(err)
	}
	if after, err := source.Recheck(ctx); err != nil || !baseline.Matches(after) {
		t.Fatal("SHM change treated as data change", err)
	}
	if err = os.WriteFile(r.path("-wal.new"), []byte("inert wal bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(r.path("-wal.new"), r.path("-wal")); err != nil {
		t.Fatal(err)
	}
	if after, err := source.Recheck(ctx); err != nil || baseline.Matches(after) || after.DB != baseline.DB {
		t.Fatal("replaced WAL with unchanged main inode matched", err)
	}
	moved := r.path("-dir") + ".moved"
	if err = os.Rename(r.path("-dir"), moved); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(r.path("-dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Recheck(ctx); err == nil {
		t.Fatal("replaced directory accepted through retained handle")
	}
	source.mu.RLock()
	closed := make(chan error, 1)
	go func() { closed <- source.Close() }()
	select {
	case <-closed:
		t.Fatal("Close invalidated handles during an in-flight check")
	case <-time.After(50 * time.Millisecond):
	}
	source.mu.RUnlock()
	if err = <-closed; err != nil {
		t.Fatal(err)
	}
	if _, err = source.Recheck(ctx); err == nil {
		t.Fatal("closed source still answers")
	}
	if _, err = source.Schema(ctx); err == nil {
		t.Fatal("closed source still observes")
	}
}
