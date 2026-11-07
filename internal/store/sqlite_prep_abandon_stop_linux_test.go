//go:build linux

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

// liveFailure leaves a claimed fresh LTX preparation recovery-required after
// its writer stop: the barrier fails and the owned replica keeps running.
func (fx *prepFixture) liveFailure(id string) *prepHost {
	fx.t.Helper()
	host := newPrepHost(fx, true)
	host.fail = "barrier"
	task, _, err := fx.prepare(host, nil, id, "")
	if !errors.Is(err, sqliteprep.ErrRecovery) || fx.state(task.OperationID) != ops.RecoveryRequired || !host.writers || host.replica != sqliteprep.Running {
		fx.t.Fatalf("live failure fixture: %v %v", err, host.calls)
	}
	host.calls, host.fail = nil, ""
	return host
}

// writersRunning leaves a claimed fresh LTX preparation recovery-required
// before its writer stop intent: writers and replica both keep running.
func (fx *prepFixture) writersRunning(id string) *prepHost {
	fx.t.Helper()
	host := newPrepHost(fx, true)
	task, _, err := fx.prepare(host, &faultJournal{SQLitePreparationJournal: fx.journal(), before: sqliteprep.WritersStopIntent}, id, "")
	if !errors.Is(err, sqliteprep.ErrRecovery) || fx.state(task.OperationID) != ops.RecoveryRequired || host.writers || host.called("stop_writers") {
		fx.t.Fatalf("running writers fixture: %v %v", err, host.calls)
	}
	host.calls = nil
	return host
}

func (fx *prepFixture) abandon(host *prepHost, journal sqliteprep.Journal, id string) (sqliteprep.Task, sqliteprep.Input, error) {
	task, in := fx.preflight(sqliteprep.AbandonTask, id, "")
	_, err := fx.service(host, journal).Abandon(context.Background(), task, in)
	return task, in, err
}

func (fx *prepFixture) stops(operation string) int {
	return fx.count("SELECT count(*) FROM data_sqlite_abandon_stops WHERE operation_id=?", operation)
}

func callCount(calls []string, name string) int {
	n := 0
	for _, c := range calls {
		if c == name {
			n++
		}
	}
	return n
}

func assertNoRemoteEffects(t *testing.T, host *prepHost) {
	t.Helper()
	for _, effect := range []string{"barrier", "verify", "upload", "capture"} {
		if host.called(effect) {
			t.Fatalf("abandon issued %s: %v", effect, host.calls)
		}
	}
}

func assertAbandoned(t *testing.T, fx *prepFixture, task sqliteprep.Task, id string) {
	t.Helper()
	p := fx.preparation(id)
	if fx.state(task.OperationID) != ops.Succeeded || !p.consumed || fx.outcome(task.OperationID) == nil {
		t.Fatalf("abandon not atomically completed: %s", fx.state(task.OperationID))
	}
	if fx.count("SELECT count(*) FROM data_fences WHERE id=? AND operation_id=? AND state='released'", p.intent.Fence.ID, p.intent.Fence.OperationID) != 1 {
		t.Fatal("original fence not released")
	}
}

// Review finding 3: a live failure after the writer stop has a local exit.
// Abandon durably records its stop intent before the only stop, then settles
// the owned replica to stopped without barrier, verification, upload or a
// credential packet, and releases only the original fence.
func TestSQLiteAbandonSettlesLiveFailureToStopped(t *testing.T) {
	fx := newPrepFixture(t)
	host := fx.liveFailure(prepID("1"))
	recorded := -1
	host.onCall = func(name string) {
		if name == "stop_replica" {
			recorded = fx.count("SELECT count(*) FROM data_sqlite_abandon_stops")
		}
	}
	task, _, err := fx.abandon(host, nil, prepID("1"))
	if err != nil || callCount(host.calls, "stop_replica") != 1 || host.called("stop_writers") {
		t.Fatalf("live failure abandon stranded: %v %s %v", err, fx.state(task.OperationID), host.calls)
	}
	assertNoRemoteEffects(t, host)
	if recorded != 1 || fx.stops(task.OperationID) != 1 {
		t.Fatalf("stop issued without a durable intent: %d", recorded)
	}
	assertAbandoned(t, fx, task, prepID("1"))
}

// Writers still running before the original stop intent are stopped first,
// then the replica, both under one recorded abandonment intent.
func TestSQLiteAbandonStopsRunningWritersThenReplica(t *testing.T) {
	fx := newPrepFixture(t)
	host := fx.writersRunning(prepID("1"))
	task, _, err := fx.abandon(host, nil, prepID("1"))
	if err != nil || callCount(host.calls, "stop_writers") != 1 || callCount(host.calls, "stop_replica") != 1 {
		t.Fatalf("running writers abandon: %v %v", err, host.calls)
	}
	writers, replica := -1, -1
	for n, c := range host.calls {
		if c == "stop_writers" {
			writers = n
		}
		if c == "stop_replica" {
			replica = n
		}
	}
	if writers > replica {
		t.Fatalf("replica stopped before writers: %v", host.calls)
	}
	assertNoRemoteEffects(t, host)
	assertAbandoned(t, fx, task, prepID("1"))
}

// An unknown writer or replica stop leaves the claimed abandonment
// recovery-required and fenced. The same key repeats nothing; an explicit new
// key settles the identical lineage while the original outcome stays.
func TestSQLiteAbandonUnknownStopStaysRecoveryRequired(t *testing.T) {
	for _, effect := range []string{"stop_writers", "stop_replica"} {
		t.Run(effect, func(t *testing.T) { testAbandonUnknownStop(t, effect) })
	}
}

func testAbandonUnknownStop(t *testing.T, effect string) {
	fx := newPrepFixture(t)
	ctx := context.Background()
	fixture := fx.liveFailure
	if effect == "stop_writers" {
		fixture = fx.writersRunning
	}
	host := fixture(prepID("1"))
	host.fail = effect
	task, in, err := fx.abandon(host, nil, prepID("1"))
	if !errors.Is(err, sqliteprep.ErrRecovery) || fx.state(task.OperationID) != ops.RecoveryRequired || !host.called(effect) {
		t.Fatalf("unknown %s settled as %s: %v", effect, fx.state(task.OperationID), err)
	}
	p := fx.preparation(prepID("1"))
	if p.consumed || fx.stops(task.OperationID) != 1 || fx.count("SELECT count(*) FROM data_sqlite_task_settlements WHERE operation_id=? AND code='effect_unknown'", task.OperationID) != 1 {
		t.Fatal("unknown stop lost its claim")
	}
	assertSQLiteFenceProtected(t, fx, p.intent.Fence)
	replay := newPrepHost(fx, false)
	if _, err = fx.service(replay, nil).Abandon(ctx, task, in); !errors.Is(err, sqliteprep.ErrRecovery) || len(replay.calls) != 0 {
		t.Fatalf("same key repeated effects: %v %v", err, replay.calls)
	}
	host.calls, host.fail = nil, ""
	again, _, err := fx.abandon(host, nil, prepID("1"))
	if err != nil {
		t.Fatalf("explicit recovery stranded: %v %v", err, host.calls)
	}
	assertNoRemoteEffects(t, host)
	assertAbandoned(t, fx, again, prepID("1"))
	if fx.state(task.OperationID) != ops.RecoveryRequired || fx.outcome(task.OperationID) == nil || fx.stops(task.OperationID) != 1 {
		t.Fatal("original recovery outcome changed")
	}
}

// Stops are admitted only for an owned live lineage without a stopped
// baseline. Each refusal settles failed before any claim or stop.
func TestSQLiteAbandonRefusesUnsafeStopsWithoutClaim(t *testing.T) {
	for name, setup := range map[string]func(*prepFixture) *prepHost{
		"restarted_replica_after_baseline": func(fx *prepFixture) *prepHost {
			fx.prepared(prepID("1"))
			host := newPrepHost(fx, true)
			host.writers = true
			return host
		},
		"restarted_writers_after_baseline": func(fx *prepFixture) *prepHost {
			fx.prepared(prepID("1"))
			return newPrepHost(fx, false)
		},
		"unowned": func(fx *prepFixture) *prepHost {
			host := fx.liveFailure(prepID("1"))
			host.unowned = true
			return host
		},
	} {
		t.Run(name, func(t *testing.T) { testAbandonRefusesUnsafeStop(t, setup) })
	}
}

func testAbandonRefusesUnsafeStop(t *testing.T, setup func(*prepFixture) *prepHost) {
	fx := newPrepFixture(t)
	host := setup(fx)
	task, _, err := fx.abandon(host, nil, prepID("1"))
	if err == nil || fx.state(task.OperationID) != ops.Failed || host.called("stop_writers") || host.called("stop_replica") {
		t.Fatalf("unsafe stop admitted: %v %s %v", err, fx.state(task.OperationID), host.calls)
	}
	p := fx.preparation(prepID("1"))
	if p.consumed || fx.stops(task.OperationID) != 0 {
		t.Fatal("refusal left a claim")
	}
	assertSQLiteFenceProtected(t, fx, p.intent.Fence)
}

// A stop-intent claim failing before its commit is a no-claim failure; a lost
// reply after its commit is a claim. Neither issues a stop.
func TestSQLiteAbandonStopClaimFaults(t *testing.T) {
	for fault, want := range map[string]ops.State{"before": ops.Failed, "after": ops.RecoveryRequired} {
		t.Run(fault, func(t *testing.T) { testAbandonStopClaimFault(t, fault, want) })
	}
}

func testAbandonStopClaimFault(t *testing.T, fault string, want ops.State) {
	fx := newPrepFixture(t)
	host := fx.liveFailure(prepID("1"))
	task, _, err := fx.abandon(host, &faultJournal{SQLitePreparationJournal: fx.journal(), abandon: fault}, prepID("1"))
	if err == nil || fx.state(task.OperationID) != want || host.called("stop_replica") {
		t.Fatalf("stop claim %s: %v %s %v", fault, err, fx.state(task.OperationID), host.calls)
	}
	if claimed := fx.stops(task.OperationID) == 1; claimed != (fault == "after") {
		t.Fatal("stop claim row does not match its settlement")
	}
	assertSQLiteFenceProtected(t, fx, fx.preparation(prepID("1")).intent.Fence)
	again, _, err := fx.abandon(host, nil, prepID("1"))
	if err != nil {
		t.Fatalf("explicit recovery after claim fault: %v", err)
	}
	assertAbandoned(t, fx, again, prepID("1"))
}

// readmitPolicyAt admits a new database policy immediately before one effect.
func (fx *prepFixture) readmitPolicyAt(effect string) func(string) {
	return func(name string) {
		if name != effect {
			return
		}
		req := dataRequest()
		req.App, req.PolicyHash = fx.app, "sha256:"+prepID("9")+prepID("9")
		if _, err := fx.s.ReserveDatabase(context.Background(), req); err != nil {
			fx.t.Error(err)
		}
	}
}

// Completion rejoins the task's own stop intent and current policy. A
// mismatch after a recorded stop is recovery-required, never failed, and a
// lost completion reply reads back the known success.
func TestSQLiteAbandonCompletionJoinsStopIntent(t *testing.T) {
	for _, mode := range []string{"strip_stop", "cross_stop", "policy"} {
		t.Run(mode, func(t *testing.T) { testAbandonCompletionJoin(t, mode) })
	}
	fx := newPrepFixture(t)
	host := fx.liveFailure(prepID("1"))
	task, _, err := fx.abandon(host, &faultJournal{SQLitePreparationJournal: fx.journal(), complete: "after"}, prepID("1"))
	if err != nil {
		t.Fatalf("lost completion reply: %v", err)
	}
	assertAbandoned(t, fx, task, prepID("1"))
}

func testAbandonCompletionJoin(t *testing.T, mode string) {
	fx := newPrepFixture(t)
	host := fx.liveFailure(prepID("1"))
	journal := &faultJournal{SQLitePreparationJournal: fx.journal(), complete: mode}
	if mode == "policy" {
		journal.complete = ""
		host.onCall = fx.readmitPolicyAt("stop_replica")
	}
	task, _, err := fx.abandon(host, journal, prepID("1"))
	if !errors.Is(err, sqliteprep.ErrRecovery) || fx.state(task.OperationID) != ops.RecoveryRequired || fx.preparation(prepID("1")).consumed {
		t.Fatalf("%s completion: %v %s", mode, err, fx.state(task.OperationID))
	}
	assertSQLiteFenceProtected(t, fx, fx.preparation(prepID("1")).intent.Fence)
	host.onCall, host.calls = nil, nil
	again, _, err := fx.abandon(host, nil, prepID("1"))
	if err != nil || host.called("stop_replica") {
		t.Fatalf("explicit recovery: %v %v", err, host.calls)
	}
	assertAbandoned(t, fx, again, prepID("1"))
}

func (fx *prepFixture) abandonStop(task sqliteprep.Task, in sqliteprep.Input, p sqlitePreparation) sqliteprep.AbandonStop {
	fx.t.Helper()
	digest, err := sqliteprep.Fingerprint(p.intent)
	if err != nil {
		fx.t.Fatal(err)
	}
	return sqliteprep.AbandonStop{Task: task, Input: in, IntentSHA256: digest, Fence: p.intent.Fence, PolicySHA256: p.intent.Scope.PolicySHA256, StopReplica: true, ClaimedAt: time.Now().UTC()}
}

func (fx *prepFixture) insertStop(operation string, s sqliteprep.AbandonStop) error {
	raw, err := sqliteprep.Encode(s)
	if err != nil {
		return err
	}
	_, err = fx.s.db.ExecContext(context.Background(), "INSERT INTO data_sqlite_abandon_stops VALUES(?,?,?,?)", operation, s.Input.PreparationID, s.Fence.ID, raw)
	return err
}

func errorContains(err error, text string) bool {
	return err != nil && strings.Contains(err.Error(), text)
}

// While one abandonment's stop intent is pending, no other abandonment may
// record a stop or consume the same preparation. Once it settles, an explicit
// new key may.
func TestSQLiteAbandonStopExcludesCompetingClaims(t *testing.T) {
	fx := newPrepFixture(t)
	host := fx.liveFailure(prepID("1"))
	p := fx.preparation(prepID("1"))
	competitor, competitorIn := fx.preflight(sqliteprep.AbandonTask, prepID("1"), "")
	var stopErr, consumerErr error
	host.onCall = func(name string) {
		if name != "stop_replica" {
			return
		}
		stopErr = fx.insertStop(competitor.OperationID, fx.abandonStop(competitor, competitorIn, p))
		consumerErr = insertReviewConsumer(fx, sqliteprep.Consumer{PreparationID: prepID("1"), OperationID: competitor.OperationID, Requester: competitor.Requester, App: competitor.App, InputSHA256: competitor.InputSHA256, Kind: sqliteprep.AbandonConsumer, PolicySHA256: p.intent.Scope.PolicySHA256, ClaimedAt: time.Now().UTC()})
	}
	host.fail = "stop_replica"
	task, _, err := fx.abandon(host, nil, prepID("1"))
	if !errors.Is(err, sqliteprep.ErrRecovery) || fx.state(task.OperationID) != ops.RecoveryRequired {
		t.Fatalf("pending stop: %v", err)
	}
	if !errorContains(stopErr, "SQLite abandon stop is not exact") || !errorContains(consumerErr, "SQLite abandon stop is pending") || fx.stops(competitor.OperationID) != 0 {
		t.Fatalf("competing claims admitted: %v / %v", stopErr, consumerErr)
	}
	host.onCall, host.fail = nil, ""
	if _, err = fx.service(host, nil).Abandon(context.Background(), competitor, competitorIn); err != nil {
		t.Fatalf("settled stop blocked explicit recovery: %v", err)
	}
	assertAbandoned(t, fx, competitor, prepID("1"))
	late, lateIn := fx.preflight(sqliteprep.AbandonTask, prepID("1"), "")
	if err = fx.insertStop(late.OperationID, fx.abandonStop(late, lateIn, p)); err == nil {
		t.Fatal("stop recorded for a consumed preparation")
	}
}

// ClaimAbandon admits only this store's live guard and the exact task,
// input, predecessor intent, original fence and current policy.
func TestSQLiteAbandonClaimRejectsCrossedJoins(t *testing.T) {
	fx := newPrepFixture(t)
	ctx := context.Background()
	host := fx.liveFailure(prepID("1"))
	p := fx.preparation(prepID("1"))
	task, in := fx.preflight(sqliteprep.AbandonTask, prepID("1"), "")
	j := fx.journal()
	g, err := fx.s.AcquireSQLiteMutationGuard(ctx, fx.reserved.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	live := &prepGuard{host: host, guard: g, scope: p.intent.Scope}
	other := newPrepFixture(t)
	foreign, err := other.s.AcquireSQLiteMutationGuard(ctx, other.reserved.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Release() }()
	for name, edit := range map[string]func(*sqliteprep.AbandonStop) sqliteprep.HostObservation{
		"crossed_requester": func(s *sqliteprep.AbandonStop) sqliteprep.HostObservation {
			s.Task.Requester = "deploy:" + prepID("b") + prepID("b")
			return live
		},
		"crossed_input": func(s *sqliteprep.AbandonStop) sqliteprep.HostObservation {
			s.Input.PreparationID = prepID("9")
			return live
		},
		"crossed_lineage": func(s *sqliteprep.AbandonStop) sqliteprep.HostObservation {
			s.IntentSHA256 = prepID("9") + prepID("9")
			return live
		},
		"crossed_fence": func(s *sqliteprep.AbandonStop) sqliteprep.HostObservation {
			s.Fence.ID = data.FenceID(prepID("9"))
			return live
		},
		"stale_policy": func(s *sqliteprep.AbandonStop) sqliteprep.HostObservation {
			s.PolicySHA256 = prepID("9") + prepID("9")
			return live
		},
		"stale_claim": func(s *sqliteprep.AbandonStop) sqliteprep.HostObservation {
			s.ClaimedAt = s.ClaimedAt.Add(-2 * claimWindow)
			return live
		},
		"hidden_guard": func(*sqliteprep.AbandonStop) sqliteprep.HostObservation { return hiddenGuard{live} },
		"foreign_guard": func(*sqliteprep.AbandonStop) sqliteprep.HostObservation {
			return &prepGuard{host: host, guard: foreign}
		},
	} {
		s := fx.abandonStop(task, in, p)
		if err := j.ClaimAbandon(ctx, s, edit(&s)); err == nil || fx.stops(task.OperationID) != 0 {
			t.Fatalf("%s admitted: %v", name, err)
		}
	}
	crossed := fx.abandonStop(task, in, p)
	crossed.Task.Requester = "deploy:" + prepID("b") + prepID("b")
	if err = fx.insertStop(task.OperationID, crossed); !errorContains(err, "not exact") {
		t.Fatalf("SQL admitted a crossed requester: %v", err)
	}
	// The replica stop needs its lifetime lock held by the running replica.
	if err = host.supervisor.Release(); err != nil {
		t.Fatal(err)
	}
	var refusal *sqliteprep.Refusal
	if err = j.ClaimAbandon(ctx, fx.abandonStop(task, in, p), live); !errors.As(err, &refusal) || fx.stops(task.OperationID) != 0 {
		t.Fatalf("stopped replica admitted a replica stop: %v", err)
	}
	host.startSupervisor()
	if err = g.Release(); err != nil {
		t.Fatal(err)
	}
	if err = j.ClaimAbandon(ctx, fx.abandonStop(task, in, p), live); err == nil {
		t.Fatal("released guard admitted a stop claim")
	}
	g, err = fx.s.AcquireSQLiteMutationGuard(ctx, fx.reserved.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Release() }()
	live.guard = g
	if err = j.ClaimAbandon(ctx, fx.abandonStop(task, in, p), live); err != nil || fx.stops(task.OperationID) != 1 {
		t.Fatalf("exact stop claim refused: %v", err)
	}
	if err = j.ClaimAbandon(ctx, fx.abandonStop(task, in, p), live); err == nil {
		t.Fatal("stop claimed twice")
	}
}

// Reads rejoin a stop row with its real task; a row carrying another task's
// record is an integrity failure that refuses settlement and every effect.
func TestSQLiteAbandonStopJoinsOnRead(t *testing.T) {
	fx := newPrepFixture(t)
	ctx := context.Background()
	host := fx.liveFailure(prepID("1"))
	host.fail = "stop_replica"
	first, _, err := fx.abandon(host, nil, prepID("1"))
	if !errors.Is(err, sqliteprep.ErrRecovery) || fx.outcome(first.OperationID) == nil {
		t.Fatalf("first abandon: %v", err)
	}
	var raw []byte
	if err = fx.s.db.QueryRowContext(ctx, "SELECT canonical FROM data_sqlite_abandon_stops WHERE operation_id=?", first.OperationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	task, in := fx.preflight(sqliteprep.AbandonTask, prepID("1"), "")
	p := fx.preparation(prepID("1"))
	if _, err = fx.s.db.ExecContext(ctx, "DROP TRIGGER data_sqlite_abandon_stops_exact"); err != nil {
		t.Fatal(err)
	}
	if _, err = fx.s.db.ExecContext(ctx, "INSERT INTO data_sqlite_abandon_stops VALUES(?,?,?,?)", task.OperationID, prepID("1"), p.intent.Fence.ID, raw); err != nil {
		t.Fatal(err)
	}
	var integrity *IntegrityError
	if _, err = fx.journal().ReadTask(ctx, task); !errors.As(err, &integrity) {
		t.Fatalf("crossed stop row read: %v", err)
	}
	if err = fx.s.FailUnclaimedSQLiteTask(ctx, task.OperationID, sqliteprep.LocalUncertain); err == nil {
		t.Fatal("crossed stop row settled failed")
	}
	replay := newPrepHost(fx, false)
	if _, err = fx.service(replay, nil).Abandon(ctx, task, in); err == nil || len(replay.calls) != 0 || fx.state(task.OperationID) != ops.Preflight {
		t.Fatalf("crossed stop row ran: %v %v", err, replay.calls)
	}
}
