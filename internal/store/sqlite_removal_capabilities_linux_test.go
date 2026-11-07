//go:build linux

package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

func TestSQLiteRemovalConsumerRejectsCrossedInputHash(t *testing.T) {
	r := newRemovalFixture(t, false)
	r.abandon(r.claim())
	ref := r.plan.Removal.Points[0]
	var raw []byte
	if err := r.fx.s.db.QueryRowContext(t.Context(), "SELECT canonical FROM data_sqlite_point_consumers WHERE preparation_id=?", ref.PreparationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	c, err := sqliteprep.DecodeConsumer(raw)
	if err != nil {
		t.Fatal(err)
	}
	c.InputSHA256 = strings.Repeat("f", 64)
	raw, err = sqliteprep.Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	execRemovalFixture(t, r.fx.s, "DROP TRIGGER data_sqlite_point_consumers_no_update")
	if _, err = r.fx.s.db.ExecContext(t.Context(), "UPDATE data_sqlite_point_consumers SET canonical=? WHERE preparation_id=?", raw, ref.PreparationID); err != nil {
		t.Fatal(err)
	}
	if _, err := readSQLitePreparation(t.Context(), r.fx.s.db, ref.PreparationID); err == nil {
		t.Fatal("crossed consumer input hash accepted by preparation history")
	}
	_, found, err := r.fx.s.ReadSQLiteRetireSuccess(t.Context(), r.task)
	if err == nil && found {
		t.Fatal("crossed consumer input hash accepted by joined success reader")
	}
}

func TestSQLiteRemovalFinishKeepsChildLifetimeThroughCommit(t *testing.T) {
	r := newRemovalFixture(t, false)
	o := r.preparedArchive(r.claim())
	old := removalReadyToFinish(t, r, o)
	at := time.Now().UTC()
	ready, done := releaseRemovalAtBoundary(t, r, r.guard.children[0].guard)
	r.fx.s.removalClock = func() time.Time {
		close(ready)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		return at
	}
	_, _, err := r.fx.s.FinishRemoval(t.Context(), r.task.ID, o, old, archive.Disposition{ArchiveID: r.plan.ArchiveID, Sequence: old.Sequence + 1, Stage: archive.Retired}, r.guard)
	if err == nil {
		t.Fatal("retirement committed after its child lifetime was released and acquired by a foreign holder")
	}
	if n := r.fx.count("SELECT count(*) FROM data_sqlite_archive_completions"); n != 0 {
		t.Fatal("refusal left terminal completion", n)
	}
}

func releaseRemovalLifetime(t *testing.T, r *removalFixture, child *SQLiteMutationGuard) error {
	if err := child.source.Load().Release(); err != nil {
		return err
	}
	foreign, err := replication.AcquireLifetimeLock(t.Context(), filepath.Join(r.fx.s.dir, "replica-locks", string(child.binding)+".lock"))
	if err != nil {
		return err
	}
	t.Cleanup(func() { _ = foreign.Release() })
	return nil
}

func releaseRemovalAtBoundary(t *testing.T, r *removalFixture, child *SQLiteMutationGuard) (chan struct{}, <-chan error) {
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		select {
		case <-ready:
			done <- releaseRemovalLifetime(t, r, child)
		case <-t.Context().Done():
		}
	}()
	return ready, done
}

func TestSQLiteRemovalClaimKeepsAllChildLifetimesThroughCommit(t *testing.T) {
	r := newRemovalFixture(t, true)
	r.preflight()
	r.hold()
	first, last := r.guard.children[0].guard, r.guard.children[1].guard
	calls := 0
	ready, done := releaseRemovalAtBoundary(t, r, first)
	host := removalHostFor(r, last)
	host.onCall = func(name string) {
		if name != "recheck" {
			return
		}
		calls++
		if calls != 2 {
			return
		}
		close(ready)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	_, err := r.fx.s.ClaimRemoval(t.Context(), r.plan, r.task.ID, r.guard)
	if calls != 2 {
		t.Fatal("final sibling recheck was not exercised", calls)
	}
	if err == nil {
		t.Fatal("two-point claim committed with an earlier child's actual lifetime held by a foreign owner")
	}
	if n := r.fx.count("SELECT count(*) FROM data_sqlite_archive_attempts"); n != 0 {
		t.Fatal("refusal left partial claim", n)
	}
}

func removalHostFor(r *removalFixture, child *SQLiteMutationGuard) *prepHost {
	for i, database := range r.databases {
		if database.reserved.Database.DatabaseID == child.database {
			return r.hosts[i]
		}
	}
	r.t.Fatal("missing exact host fixture")
	return nil
}
