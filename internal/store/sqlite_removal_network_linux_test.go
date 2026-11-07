//go:build linux

package store

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/cdn/cdntest"
	"github.com/ShaulLavo/brine/internal/spec"
)

func removalCDNFixture(t *testing.T) (*removalFixture, *cdntest.Fixture) {
	t.Helper()
	r := newRemovalFixtureMode(t, false, false)
	r.preflight()
	if err := r.fx.s.FailUnclaimedSQLiteRetire(t.Context(), r.task, "conflict"); err != nil {
		t.Fatal(err)
	}
	f := cdntest.New(t)
	current, err := r.fx.s.CurrentRelease(t.Context(), r.fx.app)
	if err != nil {
		t.Fatal(err)
	}
	_, desired, err := r.fx.s.LoadPlan(t.Context(), current.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	desired.Domains = []spec.Domain{spec.Domain(f.Attachment(f.EdgeA.Profile().Reference).Binding.Hostname)}
	in := fixture(t)
	in.Desired = desired
	r.fx.commitRelease(in, "removal-cdn-release")
	r.fx.bundle = r.originalBundles[0]
	r.prepareSources()
	r.savePlan()
	return r, f
}

func TestSQLiteRemovalCDNPlanningClaimAndTerminalGuards(t *testing.T) {
	for _, mode := range []string{"reservation", "enabled", "direct"} {
		t.Run(mode, func(t *testing.T) { checkRemovalCDN(t, mode) })
	}
}

func checkRemovalCDN(t *testing.T, mode string) {
	r, f := removalCDNFixture(t)
	setArchiveCDN(t, r.fx.s, r.plan, f, mode)
	_, _, candidateErr := r.fx.s.RemovalCandidate(t.Context(), r.fx.app)
	if mode == "reservation" {
		if candidateErr == nil {
			t.Fatal("pending CDN reservation admitted")
		}
		return
	}
	r.task = r.createTask("sqlite_retire", "", "")
	r.preflight()
	r.hold()
	o, claimErr := r.fx.s.ClaimRemoval(t.Context(), r.plan, r.task.ID, r.guard)
	if mode != "direct" {
		if candidateErr == nil || claimErr == nil {
			t.Fatal("CDN admission bypassed", mode, candidateErr, claimErr)
		}
		return
	}
	if candidateErr != nil || claimErr != nil {
		t.Fatal("direct CDN removal refused", candidateErr, claimErr)
	}
	o = r.preparedArchive(o)
	r.retire(o)
	if _, err := r.fx.s.CDNHead(t.Context(), r.fx.app); !errors.Is(err, ErrNotFound) {
		t.Fatal("direct head not atomically retired", err)
	}
}

func TestSQLiteRemovalCDNChangeAtTerminalRefuses(t *testing.T) {
	r, f := removalCDNFixture(t)
	r.task = r.createTask("sqlite_retire", "", "")
	o := r.preparedArchive(r.claim())
	old := removalReadyToFinish(t, r, o)
	original := r.task.ID
	if err := r.fx.s.MarkSQLiteRetireRecovery(t.Context(), r.task, "interrupted"); err != nil {
		t.Fatal(err)
	}
	setArchiveCDN(t, r.fx.s, r.plan, f, "enabled")
	r.task = r.createTask("sqlite_retire_resolve", original, "continue")
	r.preflight()
	_, _, err := r.fx.s.FinishRemoval(t.Context(), r.task.ID, o, old, archive.Disposition{ArchiveID: r.plan.ArchiveID, Sequence: old.Sequence + 1, Stage: archive.Retired}, r.guard)
	if err == nil || r.fx.count("SELECT count(*) FROM data_sqlite_archive_completions") != 0 {
		t.Fatal("late enabled CDN bypassed terminal admission", err)
	}
}

func TestSQLiteRemovalUDPAdmissionAndReleasedHistory(t *testing.T) {
	for _, state := range []string{"staged", "active", "releasing", "released"} {
		t.Run(state, func(t *testing.T) { checkRemovalUDP(t, state) })
	}
}

func checkRemovalUDP(t *testing.T, state string) {
	r := newRemovalFixture(t, false)
	setArchiveUDPRow(t, r.fx.s, r.fx.app, state)
	_, _, candidateErr := r.fx.s.RemovalCandidate(t.Context(), r.fx.app)
	r.preflight()
	r.hold()
	o, claimErr := r.fx.s.ClaimRemoval(t.Context(), r.plan, r.task.ID, r.guard)
	if state != "released" {
		if candidateErr == nil || claimErr == nil {
			t.Fatal("live UDP reservation admitted", state, candidateErr, claimErr)
		}
		return
	}
	if candidateErr != nil || claimErr != nil {
		t.Fatal("released UDP history refused", candidateErr, claimErr)
	}
	r.retire(r.preparedArchive(o))
}

func TestSQLiteRemovalUDPChangeAtTerminalRefuses(t *testing.T) {
	r := newRemovalFixture(t, false)
	o := r.preparedArchive(r.claim())
	old := removalReadyToFinish(t, r, o)
	setArchiveUDPRow(t, r.fx.s, r.fx.app, "active")
	_, _, err := r.fx.s.FinishRemoval(t.Context(), r.task.ID, o, old, archive.Disposition{ArchiveID: r.plan.ArchiveID, Sequence: old.Sequence + 1, Stage: archive.Retired}, r.guard)
	if err == nil {
		t.Fatal("late active UDP bypassed terminal guard")
	}
}

func TestSQLiteRemovalAbandonPreservesEnabledCDNHead(t *testing.T) {
	checkRemovalAbandonCDN(t, "enabled")
}

func checkRemovalAbandonCDN(t *testing.T, mode string) {
	r, f := removalCDNFixture(t)
	r.task = r.createTask("sqlite_retire", "", "")
	o := r.claim()
	original := r.task.ID
	if err := r.fx.s.MarkSQLiteRetireRecovery(t.Context(), r.task, "interrupted"); err != nil {
		t.Fatal(err)
	}
	setArchiveCDN(t, r.fx.s, r.plan, f, mode)
	before, headErr := r.fx.s.CDNHead(t.Context(), r.fx.app)
	reservations := r.fx.count("SELECT count(*) FROM cdn_reservations WHERE app=?", r.fx.app)
	r.task = r.createTask("sqlite_retire_resolve", original, "abandon")
	r.preflight()
	r.abandon(o)
	after, err := r.fx.s.CDNHead(t.Context(), r.fx.app)
	if (err == nil) != (headErr == nil) || !reflect.DeepEqual(before, after) || reservations != r.fx.count("SELECT count(*) FROM cdn_reservations WHERE app=?", r.fx.app) {
		t.Fatal("abandon changed CDN head/provider reservation", err)
	}
	release, err := r.fx.s.CurrentRelease(t.Context(), r.fx.app)
	if err != nil || release.ID != r.plan.Candidate.ReleaseID {
		t.Fatal("abandon changed live release", err)
	}
}
