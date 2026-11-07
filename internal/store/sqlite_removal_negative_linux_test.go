//go:build linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

func TestSQLiteRetireClaimRefusesCrossedAuthorityWithoutRows(t *testing.T) {
	for _, name := range []string{"requester", "input", "plan", "policy", "target", "credential", "location", "expiry", "changed_source", "closed", "arbitrary"} {
		t.Run(name, func(t *testing.T) { checkRemovalClaimNegative(t, name) })
	}
}

func checkRemovalClaimNegative(t *testing.T, name string) {
	r := newRemovalFixture(t, false)
	r.preflight()
	r.hold()
	p, task, hold := r.plan, r.task.ID, archive.SourceHold(r.guard)
	ref := p.Removal.Points[0]
	switch name {
	case "requester":
		id, _ := data.NewID()
		op, _, err := r.fx.s.CreateOperation(t.Context(), ops.Intent{Kind: ops.SQLiteRetire, App: r.fx.app, SecretRef: r.task.SecretRef}, "deploy:"+strings.Repeat("c", 64), id)
		if err != nil {
			t.Fatal(err)
		}
		if err = r.fx.s.TransitionOperation(t.Context(), op.ID, ops.Queued, ops.Preflight); err != nil {
			t.Fatal(err)
		}
		task = op.ID
	case "input":
		execRemovalFixture(t, r.fx.s, "DROP TRIGGER operation_identity")
		if _, err := r.fx.s.db.ExecContext(t.Context(), "UPDATE operations SET secret_ref=? WHERE id=?", strings.Repeat("f", 64), task); err != nil {
			t.Fatal(err)
		}
	case "plan":
		p.ID = "sha256:" + strings.Repeat("e", 64)
	case "policy":
		req := dataRequest()
		req.App, req.PolicyHash = r.fx.app, "sha256:"+strings.Repeat("e", 64)
		if _, err := r.fx.s.ReserveDatabase(t.Context(), req); err != nil {
			t.Fatal(err)
		}
	case "target":
		r.guard.authority.Load().target = "sha256:" + strings.Repeat("c", 64)
	case "credential":
		execRemovalFixture(t, r.fx.s, "DROP TRIGGER data_credential_records_no_update")
		if _, err := r.fx.s.db.ExecContext(t.Context(), "UPDATE data_credential_records SET canonical=json_set(canonical,'$.version',2) WHERE binding_id=? AND kind='receipt'", r.fx.reserved.Replica.BindingID); err != nil {
			t.Fatal(err)
		}
	case "location":
		execRemovalFixture(t, r.fx.s, "DROP TRIGGER data_allocations_no_update")
		if _, err := r.fx.s.db.ExecContext(t.Context(), "UPDATE data_allocations SET canonical=json_set(canonical,'$.directory_inode',999) WHERE database_id=?", ref.DatabaseID); err != nil {
			t.Fatal(err)
		}
	case "expiry":
		expired := *p.Removal
		expired.ExpiresAt = p.CreatedAt.Add(-time.Second)
		p.Removal = &expired
		p.ID, _ = p.Fingerprint()
	case "changed_source":
		r.changeWAL()
	case "closed":
		_ = r.guard.Close()
	case "arbitrary":
		hold = arbitraryRemovalHold{}
	}
	if _, err := r.fx.s.ClaimRemoval(t.Context(), p, task, hold); err == nil {
		t.Fatal("crossed authority admitted", name)
	}
	if r.fx.count("SELECT count(*) FROM data_archive_operations WHERE plan_id=?", r.plan.ID) != 0 || r.fx.count("SELECT count(*) FROM data_sqlite_archive_claims") != 0 || r.fx.count("SELECT count(*) FROM data_sqlite_point_consumers WHERE preparation_id=?", ref.PreparationID) != 0 {
		t.Fatal("refusal left a partial claim")
	}
}

type arbitraryRemovalHold struct{}

func (arbitraryRemovalHold) Recheck(context.Context) error { return nil }
func (arbitraryRemovalHold) Close() error                  { return nil }

func TestSQLiteRetireCompetitionAndGenericCompletionRefuse(t *testing.T) {
	r := newRemovalFixture(t, false)
	r.preflight()
	other := r.createTask(ops.SQLiteRetire, "", "")
	if err := r.fx.s.TransitionOperation(t.Context(), other.ID, ops.Queued, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	r.hold()
	if _, err := r.fx.s.ClaimRemoval(t.Context(), r.plan, r.task.ID, r.guard); err == nil {
		t.Fatal("competing preflight task admitted")
	}
	if err := r.fx.s.FailUnclaimedSQLiteRetire(t.Context(), other, "conflict"); err != nil {
		t.Fatal(err)
	}
	o, err := r.fx.s.ClaimRemoval(t.Context(), r.plan, r.task.ID, r.guard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.fx.s.ClaimRemoval(t.Context(), r.plan, r.task.ID, r.guard); err == nil {
		t.Fatal("second claim admitted")
	}
	if err = r.fx.s.FailUnclaimedSQLiteRetire(t.Context(), r.task, "conflict"); err == nil {
		t.Fatal("claimed task failed as unclaimed")
	}
	for _, state := range []ops.State{ops.Failed, ops.Succeeded} {
		if err = r.fx.s.CompleteTask(t.Context(), r.task.ID, state, ops.TaskOutcome{Error: "conflict"}); err == nil {
			t.Fatal("generic task completion bypassed joins", state)
		}
	}
	r.abandon(o)
}

func TestSQLiteRetireAbandonRechecksInsideFinalTransaction(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "precommit"}[late], func(t *testing.T) { checkRemovalAbandonChange(t, late) })
	}
}

func checkRemovalAbandonChange(t *testing.T, late bool) {
	r := newRemovalFixture(t, false)
	o := r.claim()
	old, err := r.fx.s.AdvanceArchiveDisposition(t.Context(), o, archive.Disposition{}, archive.Disposition{ArchiveID: r.plan.ArchiveID, Sequence: 1, Stage: archive.AbandonIntent})
	if err != nil {
		t.Fatal(err)
	}
	if !late {
		r.changeWAL()
	}
	calls := 0
	r.hosts[0].onCall = func(name string) {
		if name == "recheck" {
			calls++
		}
		if late && calls == 3 {
			r.changeWAL()
		}
	}
	_, _, err = r.fx.s.FinishRemoval(t.Context(), r.task.ID, o, old, archive.Disposition{ArchiveID: r.plan.ArchiveID, Sequence: 2, Stage: archive.Abandoned}, r.guard)
	if err == nil {
		t.Fatal("changed WAL released original fence")
	}
	if r.fx.count("SELECT count(*) FROM data_sqlite_archive_completions") != 0 || r.fx.count("SELECT count(*) FROM data_sqlite_consumer_completions") != 0 {
		t.Fatal("abandon partially committed")
	}
	ref := r.plan.Removal.Points[0]
	assertSQLiteFenceProtected(t, r.fx, ref.Fence)
}

func TestSQLiteRetireAbandonDoesNotNeedRemoteCredentials(t *testing.T) {
	r := newRemovalFixture(t, false)
	o := r.claim()
	execRemovalFixture(t, r.fx.s, "DROP TRIGGER data_credential_records_no_update")
	if _, err := r.fx.s.db.ExecContext(t.Context(), "UPDATE data_credential_records SET canonical=json_set(canonical,'$.expires_at','2000-01-01T00:00:00Z') WHERE kind='receipt'"); err != nil {
		t.Fatal(err)
	}
	receipt := r.abandon(o)
	r.assertSuccess(receipt)
}

func TestSQLiteRetireConsumerRawSQLAndProtectedReadCorruption(t *testing.T) {
	r := newRemovalFixture(t, false)
	r.preflight()
	p := r.fx.preparation(r.plan.Removal.Points[0].PreparationID)
	ref := r.plan.Removal.Points[0]
	now := time.Now().UTC()
	c := sqliteprep.Consumer{PreparationID: ref.PreparationID, OperationID: r.task.ID, Requester: r.task.Requester, App: r.task.App, InputSHA256: r.task.SecretRef, Kind: sqliteprep.ReviewedConsumer, PreparedSHA256: ref.PreparedSHA256, ReviewedPlanSHA256: strings.TrimPrefix(r.plan.ID, "sha256:"), PolicySHA256: p.intent.Scope.PolicySHA256, RetentionSHA256: ref.RetentionSHA256, Bounds: &r.plan.Removal.Bounds, ClaimedAt: now, RecoverUntil: now.Add(time.Duration(r.plan.Removal.Bounds.RecoveryWindowSeconds) * time.Second)}
	raw, err := sqliteprep.Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.fx.s.db.ExecContext(t.Context(), "INSERT INTO data_sqlite_point_consumers VALUES(?,?,?,NULL,?)", c.PreparationID, c.OperationID, c.Kind, raw); err == nil {
		t.Fatal("raw consumer without exact archive claim admitted")
	}
	r.hold()
	if _, err = r.fx.s.ClaimRemoval(t.Context(), r.plan, r.task.ID, r.guard); err != nil {
		t.Fatal(err)
	}
	execRemovalFixture(t, r.fx.s, "DROP TRIGGER data_sqlite_point_consumers_no_update")
	if _, err = r.fx.s.db.ExecContext(t.Context(), "UPDATE data_sqlite_point_consumers SET canonical=json_set(canonical,'$.requester',?) WHERE preparation_id=?", "deploy:"+strings.Repeat("b", 64), ref.PreparationID); err != nil {
		t.Fatal(err)
	}
	if _, err = readSQLitePreparation(t.Context(), r.fx.s.db, ref.PreparationID); err == nil {
		t.Fatal("protected reader trusted crossed consumer despite complete SQL rows")
	}
}

func TestSQLiteRetireImmutableMappingsRefuseReplaceUpdateDelete(t *testing.T) {
	r := newRemovalFixture(t, false)
	o := r.claim()
	tables := map[string]string{"data_sqlite_archive_attempts": "archive_operation_id", "data_sqlite_archive_claims": "preparation_id"}
	for table, column := range tables {
		assertRemovalMappingImmutable(t, r.fx.s, table, column)
	}
	receipt := r.abandon(o)
	assertRemovalMappingImmutable(t, r.fx.s, "data_sqlite_archive_completions", "archive_id")
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.fx.s.CompleteTask(t.Context(), r.task.ID, ops.Succeeded, ops.TaskOutcome{Receipt: raw}); !errors.Is(err, ErrConflict) {
		t.Fatal("generic success overwrote protected success", err)
	}
	r.assertSuccess(receipt)
}

func assertRemovalMappingImmutable(t *testing.T, s *Store, table, column string) {
	t.Helper()
	for _, statement := range []string{"UPDATE " + table + " SET " + column + "=" + column, "DELETE FROM " + table, "INSERT OR REPLACE INTO " + table + " SELECT * FROM " + table} {
		if _, err := s.db.ExecContext(t.Context(), statement); err == nil {
			t.Fatal("protected mapping mutable", statement)
		}
	}
}

func TestSQLiteRemovalGuardRejectsForeignClosedAndDuplicateSources(t *testing.T) {
	r := newRemovalFixture(t, false)
	r.hold()
	child, err := r.guard.Child(r.fx.reserved.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = child.HoldSource(t.Context()); err == nil {
		t.Fatal("second lifetime capability minted")
	}
	foreign := newPrepFixture(t)
	foreignGuard, err := foreign.s.AcquireSQLiteMutationGuard(t.Context(), foreign.reserved.Database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreignGuard.Release() }()
	foreignHold, err := foreignGuard.HoldSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreignHold.Release() }()
	source := &prepSource{guard: &prepGuard{host: newPrepHostStopped(foreign), guard: foreignGuard}, hold: foreignHold}
	if err = r.guard.AttachSource(t.Context(), r.fx.reserved.Database.DatabaseID, source); err == nil {
		t.Fatal("foreign source attached")
	}
	if err = child.source.Load().Release(); err != nil {
		t.Fatal(err)
	}
	if err = r.guard.Recheck(t.Context()); err == nil {
		t.Fatal("closed child admitted")
	}
}

func TestSQLiteRetireClaimComparesOriginalBundleNotNewHoldBaseline(t *testing.T) {
	r := newRemovalFixture(t, false)
	r.fx.bundle.WAL.Inode++
	r.preflight()
	r.hold()
	if _, err := r.fx.s.ClaimRemoval(t.Context(), r.plan, r.task.ID, r.guard); err == nil {
		t.Fatal("newly observed changed WAL replaced the protected preparation bundle")
	}
}

func TestSQLiteRetireClosedChildCannotFinishRetirement(t *testing.T) {
	r := newRemovalFixture(t, false)
	o := r.preparedArchive(r.claim())
	old := removalReadyToFinish(t, r, o)
	if err := r.guard.children[0].guard.source.Load().Release(); err != nil {
		t.Fatal(err)
	}
	_, _, err := r.fx.s.FinishRemoval(t.Context(), r.task.ID, o, old, archive.Disposition{ArchiveID: r.plan.ArchiveID, Sequence: old.Sequence + 1, Stage: archive.Retired}, r.guard)
	if err == nil {
		t.Fatal("closed child finished retirement")
	}
}

func TestSQLiteRetireDirectContinuationExpiresAtFiveMinutes(t *testing.T) {
	r := newRemovalFixture(t, false)
	o := r.preparedArchive(r.claim())
	old := removalReadyToFinish(t, r, o)
	after := time.Now().UTC().Add(6 * time.Minute)
	r.fx.s.removalClock = func() time.Time { return after }
	_, _, err := r.fx.s.FinishRemoval(t.Context(), r.task.ID, o, old, archive.Disposition{ArchiveID: r.plan.ArchiveID, Sequence: old.Sequence + 1, Stage: archive.Retired}, r.guard)
	if err == nil {
		t.Fatal("direct continuation bypassed fresh-restore recovery")
	}
}

func removalReadyToFinish(t *testing.T, r *removalFixture, o archive.Operation) archive.Disposition {
	t.Helper()
	old := archive.Disposition{}
	for i, stage := range []archive.DispositionStage{archive.CleanupIntent, archive.RuntimeRetired, archive.RenameIntent, archive.Renamed} {
		var err error
		old, err = r.fx.s.AdvanceArchiveDisposition(t.Context(), o, old, archive.Disposition{ArchiveID: r.plan.ArchiveID, Sequence: uint64(i + 1), Stage: stage})
		if err != nil {
			t.Fatal(err)
		}
	}
	return old
}

func TestSQLiteRetireFreshTTLDoesNotResetOriginalAges(t *testing.T) {
	for _, name := range []string{"backup", "verification"} {
		t.Run(name, func(t *testing.T) { checkRemovalOriginalAge(t, name) })
	}
}

func checkRemovalOriginalAge(t *testing.T, name string) {
	backup, verified := uint32(1), uint32(300)
	if name == "verification" {
		backup, verified = 86400, 1
	}
	r := newRemovalFixtureBounds(t, false, true, backup, verified)
	r.preflight()
	r.hold()
	at := r.plan.CreatedAt.Add(2 * time.Second)
	r.fx.s.removalClock = func() time.Time { return at }
	if !at.Before(r.plan.Removal.Points[0].ExpiresAt) {
		t.Fatal("fixture TTL is not fresh")
	}
	if _, err := r.fx.s.ClaimRemoval(t.Context(), r.plan, r.task.ID, r.guard); err == nil {
		t.Fatal("fresh TTL reset original source age", name)
	}
}

func TestSQLiteRemovalCandidateRejectsExpiredConsumedAndUnsettledPoints(t *testing.T) {
	for _, name := range []string{"expired", "consumed", "unrelated_fence"} {
		t.Run(name, func(t *testing.T) { checkRemovalCandidateNegative(t, name) })
	}
}

func checkRemovalCandidateNegative(t *testing.T, name string) {
	r := newRemovalFixtureMode(t, false, name != "unrelated_fence")
	switch name {
	case "expired":
		after := r.plan.Removal.Points[0].ExpiresAt.Add(time.Second)
		r.fx.s.removalClock = func() time.Time { return after }
	case "consumed":
		o := r.claim()
		r.abandon(o)
		_ = r.guard.Close()
	case "unrelated_fence":
		if _, err := r.fx.s.HoldDataFence(t.Context(), r.fx.reserved.Database.DatabaseID, "unrelated-owner"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := r.fx.s.RemovalCandidate(t.Context(), r.fx.app); err == nil {
		t.Fatal("unavailable point admitted", name)
	}
}

func TestSQLiteRemovalCandidateRefusesClaimedPreparationWithoutPoint(t *testing.T) {
	fx := newPrepFixture(t)
	host := newPrepHost(fx, true)
	journal := &faultJournal{SQLitePreparationJournal: fx.journal(), before: sqliteprep.Verified}
	if _, _, err := fx.prepare(host, journal, prepID("1"), ""); err == nil {
		t.Fatal("fault did not interrupt before point")
	}
	if _, _, err := fx.s.RemovalCandidate(t.Context(), fx.app); err == nil {
		t.Fatal("recovery-required preparation without point admitted")
	}
}

func TestSQLiteRetireProtectedReadRejectsCrossedTaskInputClaimCompletion(t *testing.T) {
	for _, name := range []string{"task", "input", "claim", "completion"} {
		t.Run(name, func(t *testing.T) { checkRemovalReadCorruption(t, name) })
	}
}

func checkRemovalReadCorruption(t *testing.T, name string) {
	r := newRemovalFixture(t, false)
	r.abandon(r.claim())
	switch name {
	case "task":
		execRemovalFixture(t, r.fx.s, "DROP TRIGGER operation_identity")
		if _, err := r.fx.s.db.ExecContext(t.Context(), "UPDATE operations SET requester=? WHERE id=?", "deploy:"+strings.Repeat("c", 64), r.task.ID); err != nil {
			t.Fatal(err)
		}
	case "input":
		execRemovalFixture(t, r.fx.s, "DROP TRIGGER data_sqlite_retire_inputs_no_update")
		if _, err := r.fx.s.db.ExecContext(t.Context(), "UPDATE data_sqlite_retire_inputs SET canonical=json_set(canonical,'$.app','other') WHERE id=?", r.task.SecretRef); err != nil {
			t.Fatal(err)
		}
	case "claim":
		execRemovalFixture(t, r.fx.s, "DROP TRIGGER data_sqlite_archive_claims_no_update")
		if _, err := r.fx.s.db.ExecContext(t.Context(), "UPDATE data_sqlite_archive_claims SET original_fence_owner='other'"); err != nil {
			t.Fatal(err)
		}
	case "completion":
		execRemovalFixture(t, r.fx.s, "DROP TRIGGER data_sqlite_archive_completions_no_update")
		original := r.fx.preparation(r.plan.Removal.Points[0].PreparationID).intent.Task.OperationID
		if _, err := r.fx.s.db.ExecContext(t.Context(), "UPDATE data_sqlite_archive_completions SET completing_operation_id=?", original); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := r.fx.s.ReadSQLiteRetireSuccess(t.Context(), r.task); err == nil {
		t.Fatal("crossed protected join trusted by success reader", name)
	}
}

func TestSQLiteRemovalGuardPartialLifetimeFailureCleansOwnedResources(t *testing.T) {
	r := newRemovalFixture(t, true)
	ids := []data.DatabaseID{r.databases[0].reserved.Database.DatabaseID, r.databases[1].reserved.Database.DatabaseID}
	slices.Sort(ids)
	var first, second *prepFixture
	for _, database := range r.databases {
		if database.reserved.Database.DatabaseID == ids[0] {
			first = database
		} else {
			second = database
		}
	}
	foreign, err := replication.AcquireLifetimeLock(t.Context(), filepath.Join(r.fx.s.dir, "replica-locks", string(second.reserved.Replica.BindingID)+".lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Release() }()
	r.guard, err = r.fx.s.AcquireSQLiteRemovalGuard(t.Context(), ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.guard.Close() })
	r.attach(first)
	child, err := r.guard.Child(ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = child.HoldSource(t.Context()); err == nil {
		t.Fatal("foreign lifetime lock did not refuse")
	}
	if err = r.guard.Close(); err != nil {
		t.Fatal(err)
	}
	held, err := replication.AcquireLifetimeLock(t.Context(), filepath.Join(r.fx.s.dir, "replica-locks", string(first.reserved.Replica.BindingID)+".lock"))
	if err != nil {
		t.Fatal("owned lifetime lock leaked after partial failure", err)
	}
	if err = held.Release(); err != nil {
		t.Fatal(err)
	}
	host, err := r.fx.s.AcquireHostLock(t.Context())
	if err != nil {
		t.Fatal("outer host lock leaked", err)
	}
	if err = host.Release(); err != nil {
		t.Fatal(err)
	}
	if held, err = replication.AcquireLifetimeLock(t.Context(), filepath.Join(r.fx.s.dir, "replica-locks", string(second.reserved.Replica.BindingID)+".lock")); err == nil {
		_ = held.Release()
		t.Fatal("cleanup released the foreign owner's lifetime lock")
	}
}
