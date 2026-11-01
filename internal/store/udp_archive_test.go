//go:build linux

package store

import (
	"errors"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
)

func setArchiveUDPRow(t *testing.T, s *Store, app, state string) {
	t.Helper()
	op := operation(t, s)
	if err := insertUDPFixture(t, s, "udp-archive", app, op, 4433); err != nil {
		t.Fatal(err)
	}
	if state == "staged" {
		return
	}
	if _, err := s.db.Exec("UPDATE udp_reservations SET state='active',owner=NULL WHERE id='udp-archive'"); err != nil {
		t.Fatal(err)
	}
	if state == "active" {
		return
	}
	if _, err := s.db.Exec("UPDATE udp_reservations SET state='releasing',owner=? WHERE id='udp-archive'", op); err != nil {
		t.Fatal(err)
	}
	if state == "releasing" {
		return
	}
	if _, err := s.db.Exec("UPDATE udp_reservations SET state='released',owner=NULL,released_by=? WHERE id='udp-archive'", op); err != nil {
		t.Fatal(err)
	}
}
func TestUDPArchiveAdmissionRefusesLiveClaimsWithoutBlockingAbandon(t *testing.T) {
	for _, state := range []string{"staged", "active", "releasing", "released"} {
		t.Run(state, func(t *testing.T) {
			s := openTest(t)
			p, o, _ := preparedStoredArchive(t, s)
			setArchiveUDPRow(t, s, p.Candidate.App, state)
			err := s.CheckArchiveUDP(t.Context(), p.Candidate.App)
			if (err == nil) != (state == "released") {
				t.Fatal(state, err)
			}
			if _, err := s.ArchiveOwnedCandidate(t.Context(), p.Candidate.App, o.ID); err != nil {
				t.Fatal("shared candidate read blocked", err)
			}
			cleanup := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: 1, Stage: archive.CleanupIntent}
			_, err = s.AdvanceArchiveDisposition(t.Context(), o, archive.Disposition{}, cleanup)
			if state == "released" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, ErrConflict) {
				t.Fatal("cleanup admitted live claim", err)
			}
			intent := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: 1, Stage: archive.AbandonIntent}
			current, err := s.AdvanceArchiveDisposition(t.Context(), o, archive.Disposition{}, intent)
			if err != nil {
				t.Fatal(err)
			}
			done := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: 2, Stage: archive.Abandoned, CommittedAt: time.Now().UTC()}
			if _, err := s.FinishArchiveDisposition(t.Context(), o, current, done); err != nil {
				t.Fatal("safe abandonment blocked", err)
			}
		})
	}
}
func TestUDPArchiveFinalTransactionRechecksLateClaim(t *testing.T) {
	for _, state := range []string{"staged", "active", "releasing", "released"} {
		t.Run(state, func(t *testing.T) {
			s := openTest(t)
			p, o, _ := preparedStoredArchive(t, s)
			current := archive.Disposition{}
			for _, stage := range []archive.DispositionStage{archive.CleanupIntent, archive.RuntimeRetired, archive.RenameIntent, archive.Renamed} {
				next := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: current.Sequence + 1, Stage: stage}
				var err error
				current, err = s.AdvanceArchiveDisposition(t.Context(), o, current, next)
				if err != nil {
					t.Fatal(err)
				}
			}
			// A separate SQLite handle installs a claim after retirement admission.
			other, err := Open(s.dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = other.Close() }()
			setArchiveUDPRow(t, other, p.Candidate.App, state)
			done := archive.Disposition{ArchiveID: p.ArchiveID, Sequence: current.Sequence + 1, Stage: archive.Retired, CommittedAt: time.Now().UTC()}
			done.RetainUntil = done.CommittedAt.Add(30 * 24 * time.Hour)
			_, err = s.FinishArchiveDisposition(t.Context(), o, current, done)
			if (err == nil) != (state == "released") {
				t.Fatal(state, err)
			}
			var live bool
			if err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM data_active_incarnations WHERE app=?)", p.Candidate.App).Scan(&live); err != nil {
				t.Fatal(err)
			}
			if live != (state != "released") {
				t.Fatal("wrong data retirement", live)
			}
		})
	}
}
func TestUDPArchiveUnreadableClaimsRefuse(t *testing.T) {
	s := openTest(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckArchiveUDP(t.Context(), "hello"); err == nil {
		t.Fatal("unreadable state admitted")
	}
}
