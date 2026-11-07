//go:build linux

package store

import (
	"testing"
	"time"
)

func TestRemovalCandidateAdmitsCurrentPreparationFence(t *testing.T) {
	fx := newPrepFixture(t)
	fx.prepared(prepID("1"))
	if _, err := fx.s.ArchiveCandidate(t.Context(), fx.app); err == nil {
		t.Fatal("legacy own-coverage path admitted prepared fence")
	}
	if _, _, err := fx.s.RemovalCandidate(t.Context(), fx.app); err != nil {
		t.Fatalf("current prepared fence must admit removal candidate: %v", err)
	}
}

func TestSQLiteRetireClaimAndAbandonJoinedReadback(t *testing.T) {
	r := newRemovalFixture(t, false)
	o := r.claim()
	receipt := r.abandon(o)
	r.assertSuccess(receipt)
	if receipt.ManifestSHA256 != "" || receipt.Outcome != "abandoned" {
		t.Fatal("pre-manifest abandon fabricated coverage")
	}
}

func TestSQLiteRetireTwoDatabasesOneTask(t *testing.T) {
	r := newRemovalFixture(t, true)
	o := r.claim()
	if len(o.Fences) != 2 || len(r.guard.children) != 2 {
		t.Fatal("two database aggregate incomplete")
	}
	receipt := r.abandon(o)
	r.assertSuccess(receipt)
	if n := r.fx.count("SELECT count(*) FROM data_sqlite_point_consumers WHERE operation_id=?", r.task.ID); n != 2 {
		t.Fatal("not atomically consumed", n)
	}
}

func TestSQLiteRetireCoverageRetirementHistoryAndMaintenanceFence(t *testing.T) {
	r := newRemovalFixture(t, false)
	o := r.preparedArchive(r.claim())
	receipt := r.retire(o)
	r.assertSuccess(receipt)
	if receipt.ManifestSHA256 == "" || receipt.Outcome != "retired" {
		t.Fatal("missing retirement proof")
	}
	ref := r.plan.Removal.Points[0]
	fence, err := r.fx.s.SQLiteArchiveFence(t.Context(), r.plan.ArchiveID, ref.DatabaseID)
	if err != nil || fence != ref.Fence {
		t.Fatal("original fence maintenance admission", fence, err)
	}
	if _, err = r.fx.s.ReadSQLitePreparedSource(t.Context(), r.fx.app, "main", ref.PreparationID); err != nil {
		t.Fatal("retired historical source unreadable", err)
	}
	if n := r.fx.count("SELECT count(*) FROM data_fences WHERE id=? AND state='held'", ref.Fence.ID); n != 1 {
		t.Fatal("retirement released original fence")
	}
	if n := r.fx.count("SELECT count(*) FROM data_active_incarnations WHERE app=?", r.fx.app); n != 0 {
		t.Fatal("retirement preserved live incarnation")
	}
}

func TestSQLiteRetireSuccessorOriginHistoryAfterRetire(t *testing.T) {
	r := newRemovalFixture(t, false)
	if _, _, err := r.fx.prepare(newPrepHostStopped(r.fx), nil, prepID("2"), prepID("1")); err != nil {
		t.Fatal(err)
	}
	r.savePlan()
	r.task = r.createTask("sqlite_retire", "", "")
	if r.plan.Removal.Points[0].SourcePointID != prepID("1") || r.plan.Removal.Points[0].PreparationID != prepID("2") {
		t.Fatal("successor lost original source identity")
	}
	o := r.preparedArchive(r.claim())
	r.assertSuccess(r.retire(o))
	for _, id := range []string{prepID("1"), prepID("2")} {
		if _, err := r.fx.s.ReadSQLitePreparedSource(t.Context(), r.fx.app, "main", id); err != nil {
			t.Fatal("origin/successor history unreadable", id, err)
		}
	}
}

func TestSQLiteRetireAbandonAfterPointExpiryKeepsHistoryReadable(t *testing.T) {
	r := newRemovalFixture(t, false)
	o := r.claim()
	at := r.plan.Removal.Points[0].ExpiresAt.Add(time.Second)
	r.fx.s.removalClock = func() time.Time { return at }
	r.assertSuccess(r.abandon(o))
	if _, err := r.fx.s.ReadSQLitePreparedSource(t.Context(), r.fx.app, "main", r.plan.Removal.Points[0].PreparationID); err != nil {
		t.Fatal("abandon/expiry destroyed historical source", err)
	}
}
