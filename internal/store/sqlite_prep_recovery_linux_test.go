//go:build linux

package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

func (fx *prepFixture) journal() *SQLitePreparationJournal {
	return fx.s.SQLitePreparationJournal(fx.observeLocation)
}

// R1 test 9: Q claims reverify and is interrupted before its own stopped event.
// R inherits P's baseline and exact coverage; a changed WAL refuses instead.
func TestSQLiteInheritedBaselineSurvivesInterruptedSuccessor(t *testing.T) {
	// Q's own matching stopped event, once recorded, is the effective baseline.
	for interrupted, baseline := range map[sqliteprep.Stage]string{sqliteprep.WritersStopped: prepID("1"), sqliteprep.ReplicaStopped: prepID("1"), sqliteprep.CoverageRetained: prepID("2")} {
		t.Run(string(interrupted), func(t *testing.T) { testInheritedBaseline(t, interrupted, baseline) })
	}
}

func testInheritedBaseline(t *testing.T, interrupted sqliteprep.Stage, baseline string) {
	fx := newPrepFixture(t)
	fx.prepared(prepID("1"))
	origin := fx.preparation(prepID("1"))
	q, _, err := fx.prepare(newPrepHostStopped(fx), &faultJournal{SQLitePreparationJournal: fx.journal(), before: interrupted}, prepID("2"), prepID("1"))
	if !errors.Is(err, sqliteprep.ErrRecovery) || fx.state(q.OperationID) != ops.RecoveryRequired {
		t.Fatalf("interrupted successor: %v %s", err, fx.state(q.OperationID))
	}
	changed := newPrepHostStopped(fx)
	changed.bundle.WAL.Inode++
	refused, _, err := fx.prepare(changed, nil, prepID("3"), prepID("2"))
	if err == nil || fx.state(refused.OperationID) != ops.Failed || fx.preparation(prepID("2")).consumed || fx.count("SELECT count(*) FROM data_sqlite_preparations WHERE id=?", prepID("3")) != 0 {
		t.Fatalf("changed WAL blessed: %v", err)
	}
	host := newPrepHostStopped(fx)
	if _, _, err = fx.prepare(host, nil, prepID("4"), prepID("2")); err != nil {
		t.Fatal(err)
	}
	r := fx.preparation(prepID("4"))
	if r.intent.Mode != sqliteprep.SupersedeReverify || r.intent.CoverageOrigin != prepID("1") || r.intent.BaselineFrom != baseline || !reflect.DeepEqual(r.point.Source, origin.point.Source) || host.called("upload") || host.called("barrier") {
		t.Fatalf("inheritance lost: %+v %v", r.intent, host.calls)
	}
	assertSQLiteFenceProtected(t, fx, origin.intent.Fence)
}

// R1 tests 5 and 10: a fresh snapshot interrupted before its upload intent has
// a stopped baseline and no coverage. A capture successor inherits it and
// uploads under its own key; changed authoritative data refuses before claim.
func TestSQLiteCaptureInheritsBaselineWithoutCoverage(t *testing.T) {
	fx := newPrepFixture(t)
	p, _, err := fx.prepare(newPrepHost(fx, false), &faultJournal{SQLitePreparationJournal: fx.journal(), before: sqliteprep.CoverageIntent}, prepID("1"), "")
	if !errors.Is(err, sqliteprep.ErrRecovery) || fx.state(p.OperationID) != ops.RecoveryRequired {
		t.Fatalf("interrupted fresh snapshot: %v", err)
	}
	q, _, err := fx.prepare(newPrepHostStopped(fx), &faultJournal{SQLitePreparationJournal: fx.journal(), before: sqliteprep.ReplicaStopped}, prepID("2"), prepID("1"))
	if !errors.Is(err, sqliteprep.ErrRecovery) || fx.preparation(prepID("2")).intent.BaselineFrom != prepID("1") {
		t.Fatalf("capture claim did not inherit baseline: %v %s", err, fx.state(q.OperationID))
	}
	changed := newPrepHostStopped(fx)
	changed.bundle.DB.Size++
	if _, _, err = fx.prepare(changed, nil, prepID("3"), prepID("2")); err == nil || changed.called("capture") {
		t.Fatal("changed data captured under inherited baseline")
	}
	host := newPrepHostStopped(fx)
	if _, _, err = fx.prepare(host, nil, prepID("4"), prepID("2")); err != nil {
		t.Fatal(err)
	}
	r := fx.preparation(prepID("4"))
	if r.intent.Mode != sqliteprep.SupersedeCapture || r.intent.BaselineFrom != prepID("1") || r.intent.CoverageOrigin != "" || r.point.Source.Origin != prepID("4") || !host.called("upload") {
		t.Fatalf("capture successor: %+v", r.intent)
	}
	if r.point.Source.Point.Snapshot.ObjectKey != sqliteprep.SnapshotKey(r.intent.Scope, prepID("4")) {
		t.Fatal("capture reused an old key")
	}
}

// R1 test 13: a committed completion whose reply is lost reads back its known
// success; a completion fault before commit stays fenced recovery-required.
func TestSQLiteCompletionFaultsReadBackJoinedOutcome(t *testing.T) {
	fx := newPrepFixture(t)
	task, result, err := fx.prepare(newPrepHost(fx, true), &faultJournal{SQLitePreparationJournal: fx.journal(), complete: "after"}, prepID("1"), "")
	if err != nil || result.Receipt.Outcome != sqliteprep.PreparedOutcome || fx.state(task.OperationID) != ops.Succeeded {
		t.Fatalf("lost reply did not read back success: %v", err)
	}
	other := newPrepFixture(t)
	failed, _, err := other.prepare(newPrepHost(other, true), &faultJournal{SQLitePreparationJournal: other.journal(), complete: "before"}, prepID("1"), "")
	if !errors.Is(err, sqliteprep.ErrRecovery) || other.state(failed.OperationID) != ops.RecoveryRequired {
		t.Fatalf("precommit fault: %v", err)
	}
	p := other.preparation(prepID("1"))
	if p.point != nil {
		t.Fatal("point-shaped row without completion")
	}
	outcome, err := other.s.ReadSQLiteTaskOutcome(context.Background(), failed.OperationID)
	if err != nil || outcome == nil || outcome.Error == "" {
		t.Fatalf("recovery readback: %+v %v", outcome, err)
	}
	assertSQLiteFenceProtected(t, other, p.intent.Fence)
}

func TestSQLiteAbandonReleasesOnlyOriginalFence(t *testing.T) {
	fx := newPrepFixture(t)
	prepare, _ := fx.prepared(prepID("1"))
	origin := fx.preparation(prepID("1"))
	ctx := context.Background()
	task, in := fx.preflight(sqliteprep.AbandonTask, prepID("1"), "")
	host := newPrepHostStopped(fx)
	result, err := fx.service(host, nil).Abandon(ctx, task, in)
	if err != nil || result.Receipt.Outcome != sqliteprep.AbandonedOutcome || result.Receipt.SourcePointID != origin.point.Source.Point.ID {
		t.Fatalf("abandon: %+v %v", result, err)
	}
	for _, effect := range []string{"barrier", "capture", "upload", "stop_writers", "stop_replica"} {
		if host.called(effect) {
			t.Fatalf("abandon issued %s", effect)
		}
	}
	if fx.count("SELECT count(*) FROM data_fences WHERE id=? AND operation_id=? AND state='released'", origin.intent.Fence.ID, prepare.OperationID) != 1 {
		t.Fatal("original fence not released")
	}
	if outcome, err := fx.s.ReadSQLiteTaskOutcome(ctx, task.OperationID); err != nil || outcome == nil {
		t.Fatalf("abandon readback: %v", err)
	}
	// The historical prepare completion stays valid after legitimate consumption.
	if outcome, err := fx.s.ReadSQLiteTaskOutcome(ctx, prepare.OperationID); err != nil || outcome == nil {
		t.Fatalf("historical prepare readback: %v", err)
	}
	again, againIn := fx.preflight(sqliteprep.AbandonTask, prepID("1"), "")
	if _, err = fx.service(newPrepHostStopped(fx), nil).Abandon(ctx, again, againIn); err == nil || fx.state(again.OperationID) != ops.Failed {
		t.Fatalf("consumed point abandoned twice: %v", err)
	}
	if _, _, err = fx.prepare(newPrepHostStopped(fx), nil, prepID("2"), prepID("1")); err == nil {
		t.Fatal("abandoned point superseded")
	}
}

func TestSQLiteAbandonTypedStoreRefusalSettlesFailedUnclaimed(t *testing.T) {
	fx := newPrepFixture(t)
	fx.prepared(prepID("1"))
	task, in := fx.preflight(sqliteprep.AbandonTask, prepID("1"), "")
	_, err := fx.service(newPrepHostStopped(fx), &faultJournal{SQLitePreparationJournal: fx.journal(), complete: "release_hold"}).Abandon(context.Background(), task, in)
	var refusal *sqliteprep.Refusal
	if !errors.As(err, &refusal) || fx.state(task.OperationID) != ops.Failed || fx.preparation(prepID("1")).consumed {
		t.Fatalf("typed no-effect refusal: %v %s", err, fx.state(task.OperationID))
	}
	assertSQLiteFenceProtected(t, fx, fx.preparation(prepID("1")).intent.Fence)
	changed := newPrepHostStopped(fx)
	changed.bundle.WAL.Inode++
	task, in = fx.preflight(sqliteprep.AbandonTask, prepID("1"), "")
	if _, err = fx.service(changed, nil).Abandon(context.Background(), task, in); err == nil || fx.state(task.OperationID) != ops.Failed {
		t.Fatalf("changed source abandoned: %v", err)
	}
}

func TestSQLiteClaimRequiresStoreMintedLiveGuard(t *testing.T) {
	fx := newPrepFixture(t)
	hidden := newPrepHost(fx, true)
	hidden.hide = true
	task, _, err := fx.prepare(hidden, nil, prepID("1"), "")
	if err == nil || fx.state(task.OperationID) != ops.Failed || hidden.called("stop_writers") || fx.count("SELECT count(*) FROM data_sqlite_preparations") != 0 || fx.count("SELECT count(*) FROM data_fences WHERE state='held'") != 0 {
		t.Fatalf("arbitrary port authorized a claim: %v", err)
	}
	foreign := newPrepFixture(t)
	ctx := context.Background()
	guard, err := foreign.s.AcquireSQLiteMutationGuard(ctx, foreign.reserved.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guard.Release() }()
	if guard.live(fx.s, fx.reserved.Database.DatabaseID) || guard.live(foreign.s, fx.reserved.Database.DatabaseID) {
		t.Fatal("foreign or crossed guard accepted")
	}
	if err = guard.Release(); err != nil || guard.live(foreign.s, foreign.reserved.Database.DatabaseID) {
		t.Fatal("released guard still live")
	}
	if _, err = guard.HoldSource(ctx); err == nil {
		t.Fatal("released guard minted a source hold")
	}
}

func TestSQLiteTaskJoinsRefuseCrossedIdentity(t *testing.T) {
	fx := newPrepFixture(t)
	ctx := context.Background()
	task, _ := fx.prepared(prepID("1"))
	j := fx.journal()
	crossed := task
	crossed.Requester = "deploy:" + prepID("b") + prepID("b")
	if _, err := j.ReadTask(ctx, crossed); err == nil {
		t.Fatal("crossed requester read")
	}
	crossed = task
	crossed.InputSHA256 = prepID("c") + prepID("c")
	if _, err := j.ReadTask(ctx, crossed); err == nil {
		t.Fatal("crossed input hash read")
	}
	if _, err := fx.s.SaveSQLiteTaskInput(ctx, sqliteprep.Input{Kind: sqliteprep.PrepareTask, PreparationID: prepID("2"), App: "other", DatabaseID: fx.reserved.Database.DatabaseID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("crossed app input: %v", err)
	}
	pending, _ := fx.preflight(sqliteprep.PrepareTask, prepID("3"), prepID("1"))
	if err := fx.s.CompleteTask(ctx, pending.OperationID, ops.Failed, ops.TaskOutcome{Error: "conflict"}); !errors.Is(err, ErrConflict) || fx.state(pending.OperationID) != ops.Preflight {
		t.Fatalf("generic failure completion: %v", err)
	}
	if err := fx.s.CompleteTask(ctx, pending.OperationID, ops.Succeeded, ops.TaskOutcome{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("generic success completion: %v", err)
	}
	// Corrupted canonical bytes refuse on protected reads.
	if _, err := fx.s.db.ExecContext(ctx, "DROP TRIGGER data_sqlite_prepared_points_no_update"); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.s.db.ExecContext(ctx, "UPDATE data_sqlite_prepared_points SET canonical=CAST(canonical||' ' AS BLOB)"); err != nil {
		t.Fatal(err)
	}
	var integrity *IntegrityError
	if _, err := fx.s.ReadSQLiteTaskOutcome(ctx, task.OperationID); !errors.As(err, &integrity) {
		t.Fatalf("noncanonical point read: %v", err)
	}
}

func TestSQLiteConsumersAreExclusive(t *testing.T) {
	fx := newPrepFixture(t)
	fx.prepared(prepID("1"))
	if _, _, err := fx.prepare(newPrepHostStopped(fx), nil, prepID("2"), prepID("1")); err != nil {
		t.Fatal(err)
	}
	loser, _, err := fx.prepare(newPrepHostStopped(fx), nil, prepID("3"), prepID("1"))
	if err == nil || fx.state(loser.OperationID) != ops.Failed {
		t.Fatalf("second successor consumed the same point: %v", err)
	}
	ctx := context.Background()
	if _, err = fx.s.db.ExecContext(ctx, "INSERT INTO data_sqlite_point_consumers SELECT preparation_id,?,'abandon',NULL,canonical FROM data_sqlite_point_consumers LIMIT 1", loser.OperationID); err == nil {
		t.Fatal("point consumed twice")
	}
	// An active reviewed consumer and an unused point both protect the fence.
	q := fx.preparation(prepID("2"))
	other, _ := fx.preflight(sqliteprep.AbandonTask, prepID("2"), "")
	digest, err := sqliteprep.Fingerprint(*q.point)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reviewed, err := sqliteprep.Encode(sqliteprep.Consumer{PreparationID: prepID("2"), OperationID: other.OperationID, Requester: prepRequester, App: fx.app, InputSHA256: other.InputSHA256, Kind: sqliteprep.ReviewedConsumer, PreparedSHA256: digest, ReviewedPlanSHA256: strings.Repeat("9", 64), PolicySHA256: q.intent.Scope.PolicySHA256, RetentionSHA256: q.intent.Scope.Retention.EvidenceSHA256, Bounds: &data.InitializationBounds{MaxBackupAgeSeconds: 120, MaxRestoreTestAgeSeconds: 30, RecoveryWindowSeconds: 3600}, ClaimedAt: now, RecoverUntil: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fx.s.db.ExecContext(ctx, "INSERT INTO data_sqlite_point_consumers VALUES(?,?,'reviewed_mutation',NULL,?)", prepID("2"), other.OperationID, reviewed); err != nil {
		t.Fatal(err)
	}
	if !fx.preparation(prepID("2")).consumed {
		t.Fatal("canonical reviewed consumer not joined")
	}
	if _, err = fx.s.db.ExecContext(ctx, "INSERT INTO data_sqlite_consumer_completions VALUES(?,?,?)", prepID("1"), other.OperationID, q.intent.Fence.ID); err == nil {
		t.Fatal("foreign completion admitted while a reviewed consumer is active")
	}
	assertSQLiteFenceProtected(t, fx, q.intent.Fence)
	abandon, in := fx.preflight(sqliteprep.AbandonTask, prepID("2"), "")
	if _, err = fx.service(newPrepHostStopped(fx), nil).Abandon(ctx, abandon, in); err == nil {
		t.Fatal("abandon released a fence held by an active consumer")
	}
}

// Every journal intent of the fresh LTX path, failing before its commit or
// losing its reply after, stays claimed, fenced and recovery-required. The
// same task never replays an effect afterwards.
func TestSQLitePrepareIntentFaultsStayFencedWithoutReplay(t *testing.T) {
	stages := []sqliteprep.Stage{sqliteprep.WritersStopIntent, sqliteprep.WritersStopped, sqliteprep.CoverageUploaded, sqliteprep.Verified, sqliteprep.ReplicaStopIntent, sqliteprep.ReplicaStopped}
	for _, stage := range stages {
		t.Run(string(stage)+"/before", func(t *testing.T) { testIntentFault(t, &faultJournal{before: stage}) })
		t.Run(string(stage)+"/after", func(t *testing.T) { testIntentFault(t, &faultJournal{after: stage}) })
	}
}

func testIntentFault(t *testing.T, journal *faultJournal) {
	fx := newPrepFixture(t)
	journal.SQLitePreparationJournal = fx.journal()
	task, in := fx.preflight(sqliteprep.PrepareTask, prepID("1"), "")
	if _, err := fx.service(newPrepHost(fx, true), journal).Prepare(context.Background(), task, in); !errors.Is(err, sqliteprep.ErrRecovery) {
		t.Fatalf("fault not recovery: %v", err)
	}
	p := fx.preparation(prepID("1"))
	if fx.state(task.OperationID) != ops.RecoveryRequired || p.point != nil || p.consumed {
		t.Fatal("faulted preparation not retained")
	}
	assertSQLiteFenceProtected(t, fx, p.intent.Fence)
	replay := newPrepHost(fx, false)
	if _, err := fx.service(replay, nil).Prepare(context.Background(), task, in); !errors.Is(err, sqliteprep.ErrRecovery) || len(replay.calls) != 0 {
		t.Fatalf("same task replayed effects: %v %v", err, replay.calls)
	}
}

func TestSQLiteClaimLostReplyIsClaimedRecovery(t *testing.T) {
	fx := newPrepFixture(t)
	host := newPrepHost(fx, true)
	task, _, err := fx.prepare(host, &faultJournal{SQLitePreparationJournal: fx.journal(), claimLost: true}, prepID("1"), "")
	if !errors.Is(err, sqliteprep.ErrRecovery) || fx.state(task.OperationID) != ops.RecoveryRequired || host.called("stop_writers") {
		t.Fatalf("lost claim reply: %v %v", err, host.calls)
	}
	if fx.count("SELECT count(*) FROM data_sqlite_task_settlements WHERE operation_id=? AND code='effect_unknown'", task.OperationID) != 1 {
		t.Fatal("claimed uncertainty not recorded")
	}
	assertSQLiteFenceProtected(t, fx, fx.preparation(prepID("1")).intent.Fence)
}

// A later policy admission no longer authorizes a new attempt for the held
// source, but it must not strand a safe local abandon of that source.
func TestSQLitePolicyReadmissionRefusesSupersedeButNotAbandon(t *testing.T) {
	fx := newPrepFixture(t)
	fx.prepared(prepID("1"))
	ctx := context.Background()
	req := dataRequest()
	req.App, req.PolicyHash = fx.app, "sha256:"+strings.Repeat("9", 64)
	if _, err := fx.s.ReserveDatabase(ctx, req); err != nil {
		t.Fatal(err)
	}
	host := newPrepHostStopped(fx)
	successor, _, err := fx.prepare(host, nil, prepID("2"), prepID("1"))
	if err == nil || fx.state(successor.OperationID) != ops.Failed || host.called("verify") || fx.preparation(prepID("1")).consumed {
		t.Fatalf("readmitted policy authorized a successor: %v", err)
	}
	task, in := fx.preflight(sqliteprep.AbandonTask, prepID("1"), "")
	if _, err = fx.service(newPrepHostStopped(fx), nil).Abandon(ctx, task, in); err != nil || fx.state(task.OperationID) != ops.Succeeded {
		t.Fatalf("abandon stranded by policy readmission: %v", err)
	}
	var raw []byte
	if err = fx.s.db.QueryRowContext(ctx, "SELECT canonical FROM data_sqlite_point_consumers WHERE operation_id=?", task.OperationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if consumer, err := sqliteprep.DecodeConsumer(raw); err != nil || consumer.PolicySHA256 != strings.Repeat("9", 64) {
		t.Fatalf("abandon consumer policy: %+v %v", consumer, err)
	}
}
