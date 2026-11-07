//go:build linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestSQLiteRetireRunnerPreclaimFailureAndClaimedRecovery(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unclaimed", true: "claimed"}[claimed], func(t *testing.T) { checkRemovalRunnerSettlement(t, claimed) })
	}
}

func checkRemovalRunnerSettlement(t *testing.T, claimed bool) {
	r := newRemovalFixture(t, false)
	runner := jobs.Runner{Store: r.fx.s, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.SQLiteRetire: func(ctx context.Context, current ops.Operation) (json.RawMessage, error) {
		if err := r.runnerClaim(ctx, current, claimed); err != nil {
			return nil, err
		}
		return nil, context.Canceled
	}}}
	err := runner.Run(t.Context(), r.task.ID)
	wanted := ops.Failed
	if claimed {
		wanted = ops.RecoveryRequired
	}
	if !errors.Is(err, context.Canceled) || r.fx.state(r.task.ID) != wanted {
		t.Fatal("wrong settlement", r.fx.state(r.task.ID), err)
	}
	if _, err = r.fx.s.ReadSQLiteRetireOutcome(t.Context(), r.task.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteRetireRunnerLostReplyAndResolverCompletesOnlyItself(t *testing.T) {
	r := newRemovalFixture(t, false)
	o := r.claim()
	if err := r.fx.s.MarkSQLiteRetireRecovery(t.Context(), r.task, "interrupted"); err != nil {
		t.Fatal(err)
	}
	original := r.task
	r.task = r.createTask(ops.SQLiteRetireResolve, original.ID, "abandon")
	runner := jobs.Runner{Store: r.fx.s, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.SQLiteRetireResolve: func(context.Context, ops.Operation) (json.RawMessage, error) {
		r.abandon(o)
		return nil, errPrepFault
	}}}
	if err := runner.Run(t.Context(), r.task.ID); !errors.Is(err, errPrepFault) {
		t.Fatal("lost reply not exercised", err)
	}
	if r.fx.state(original.ID) != ops.RecoveryRequired || r.fx.state(r.task.ID) != ops.Succeeded {
		t.Fatal("resolver overwrote original or lost committed result")
	}
	stored, err := r.fx.s.ReadSQLiteRetireOutcome(t.Context(), r.task.ID)
	if err != nil || stored == nil {
		t.Fatal("missing joined completion", err)
	}
	called := false
	runner.TaskHandlers[ops.SQLiteRetireResolve] = func(context.Context, ops.Operation) (json.RawMessage, error) { called = true; return nil, errPrepFault }
	if err = runner.Run(t.Context(), r.task.ID); err != nil || called {
		t.Fatal("lost-reply readback reran handler", called, err)
	}
	before := r.fx.count("SELECT count(*) FROM operation_outcomes")
	for _, state := range []ops.State{ops.Succeeded, ops.Failed} {
		if err = r.fx.s.CompleteTask(t.Context(), r.task.ID, state, ops.TaskOutcome{Error: result.Conflict}); err == nil {
			t.Fatal("generic resolver completion admitted", state)
		}
	}
	if after := r.fx.count("SELECT count(*) FROM operation_outcomes"); after != before {
		t.Fatal("generic completion changed history")
	}
}

func TestSQLiteRetireNeverWrittenNoPointUsesProtectedAttempt(t *testing.T) {
	r := newRemovalFixtureMode(t, false, false)
	if len(r.plan.Removal.Points) != 0 {
		t.Fatal("invented empty preparation")
	}
	o := r.claim()
	if len(o.Fences) != 1 || o.Fences[0].OperationID != o.ID {
		t.Fatal("missing empty archive-owned fence")
	}
	if n := r.fx.count("SELECT count(*) FROM data_sqlite_archive_attempts WHERE archive_operation_id=? AND task_operation_id=?", o.ID, r.task.ID); n != 1 {
		t.Fatal("no-point attempt not protected")
	}
	receipt := r.abandon(o)
	r.assertSuccess(receipt)
	if n := r.fx.count("SELECT count(*) FROM data_sqlite_point_consumers"); n != 0 {
		t.Fatal("no-point branch fabricated a consumer")
	}
}

func TestSQLiteRetireNoPointRejectsPresentSource(t *testing.T) {
	r := newRemovalFixtureMode(t, false, false)
	r.fx.bundle.DB = r.fx.bundle.WAL
	r.fx.bundle.DB.Presence = "regular"
	r.fx.bundle.DB.Device, r.fx.bundle.DB.Inode, r.fx.bundle.DB.MountID = 7, 21, 11
	r.fx.bundle.DB.UID, r.fx.bundle.DB.GID = r.fx.location.UID, r.fx.location.GID
	r.fx.bundle.DB.Mode, r.fx.bundle.DB.Links, r.fx.bundle.DB.Size = 0600, 1, 8192
	r.fx.bundle.DB.MtimeNS = 1
	r.fx.bundle.DB.ProofSHA256 = sqliteHash([]byte("unexpected database"))
	r.preflight()
	r.hold()
	if _, err := r.fx.s.ClaimRemoval(t.Context(), r.plan, r.task.ID, r.guard); err == nil {
		t.Fatal("metadata-only absence granted physical authority")
	}
}

func (r *removalFixture) runnerClaim(ctx context.Context, current ops.Operation, claimed bool) error {
	if !claimed {
		return nil
	}
	r.hold()
	_, err := r.fx.s.ClaimRemoval(ctx, r.plan, current.ID, r.guard)
	return err
}

func TestSQLiteRetireGenericCompletionCannotMintAnyTerminalOutcome(t *testing.T) {
	r := newRemovalFixture(t, false)
	r.preflight()
	assertGenericRemovalCompletionRefused(t, r)
	r.hold()
	o, err := r.fx.s.ClaimRemoval(t.Context(), r.plan, r.task.ID, r.guard)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.fx.s.MarkSQLiteRetireRecovery(t.Context(), r.task, "interrupted"); err != nil {
		t.Fatal(err)
	}
	r.task = r.createTask(ops.SQLiteRetireResolve, r.task.ID, "abandon")
	r.preflight()
	assertGenericRemovalCompletionRefused(t, r)
	r.abandon(o)
}

func assertGenericRemovalCompletionRefused(t *testing.T, r *removalFixture) {
	t.Helper()
	attempt := r.task.ID
	if r.task.Kind == ops.SQLiteRetireResolve {
		attempt = r.task.RecoveryOf
	}
	forged := archive.RemovalReceipt{Format: "brine-sqlite-archive-receipt-v1", OperationID: r.task.ID, AttemptOperationID: attempt, PlanID: r.plan.ID, ArchiveID: r.plan.ArchiveID, CompletionSHA256: strings.Repeat("a", 64), Outcome: "abandoned"}
	raw, err := archive.EncodeRemovalReceipt(forged)
	if err != nil {
		t.Fatal("forged shape must be syntactically valid", err)
	}
	if err = r.fx.s.CompleteTask(t.Context(), r.task.ID, ops.Succeeded, ops.TaskOutcome{Receipt: raw}); err == nil {
		t.Fatal("generic success minted a protected receipt")
	}
	if err = r.fx.s.CompleteTask(t.Context(), r.task.ID, ops.Failed, ops.TaskOutcome{Error: result.Conflict}); err == nil {
		t.Fatal("generic failure bypassed no-claim check")
	}
	if r.fx.state(r.task.ID) != ops.Preflight {
		t.Fatal("generic completion changed protected task state")
	}
}

func TestSQLiteRetireNoPointFenceCannotReleaseWithoutArchiveCompletion(t *testing.T) {
	r := newRemovalFixtureMode(t, false, false)
	o := r.claim()
	fence := o.Fences[0]
	if err := r.fx.s.ReleaseDataFence(t.Context(), fence.ID, fence.OperationID); err == nil {
		t.Fatal("generic release bypassed no-point archive ownership")
	}
	if _, err := r.fx.s.db.ExecContext(t.Context(), "UPDATE data_fences SET state='released' WHERE id=?", fence.ID); err == nil {
		t.Fatal("raw release bypassed no-point archive completion")
	}
	r.assertSuccess(r.abandon(o))
}
