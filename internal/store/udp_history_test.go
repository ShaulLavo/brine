//go:build linux

package store

import (
	"fmt"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

func TestReleasedUDPHistoryIsReadOnlyScopedAndNotPlanningOwnership(t *testing.T) {
	s := openTest(t)
	op := operation(t, s)
	if err := insertUDPFixture(t, s, "udp-old", "hello", op, 4433); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE udp_reservations SET state='released',owner=NULL,released_by=? WHERE id='udp-old'", op); err != nil {
		t.Fatal(err)
	}
	before, err := s.Generation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.UDPHistory(t.Context(), "hello")
	if err != nil || !history.Complete || len(history.Endpoints) != 1 || history.Endpoints[0].HostPort != 4433 {
		t.Fatal(history, err)
	}
	other, err := s.UDPHistory(t.Context(), "other")
	if err != nil || !other.Complete || len(other.Endpoints) != 0 {
		t.Fatal(other, err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	state, err := readUDPState(t.Context(), tx)
	_ = tx.Rollback()
	if err != nil || len(state.Reservations) != 0 {
		t.Fatal("released history entered planning ownership", state, err)
	}
	after, err := s.Generation(t.Context())
	if err != nil || after != before {
		t.Fatal(after, before, err)
	}
	if _, err := s.UDPHistory(t.Context(), "not valid"); err == nil {
		t.Fatal("unbounded invalid app query")
	}
}

func TestUDPHistoryBoundAndInventoryUnknown(t *testing.T) {
	s := openTest(t)
	r := release(t, s, "history-release")
	if err := s.CommitRelease(t.Context(), "hello", r); err != nil {
		t.Fatal(err)
	}
	op := operation(t, s)
	for i := 0; i < 65; i++ {
		id := fmt.Sprintf("udp-history-%d", i)
		if err := insertUDPFixture(t, s, id, "hello", op, 4433); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("UPDATE udp_reservations SET state='released',owner=NULL,released_by=? WHERE id=?", op, id); err != nil {
			t.Fatal(err)
		}
	}
	history, err := s.UDPHistory(t.Context(), "hello")
	if err != nil || history.Complete || len(history.Endpoints) != 1 {
		t.Fatal(history, err)
	}
	inventory, err := s.InventoryState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, app := range inventory.Apps {
		if app.Name == "hello" {
			found = true
		}
		if app.Name == "hello" && (app.RetiredUDP == nil || app.RetiredUDP.Status != target.Unknown) {
			t.Fatal("truncated history lost", app)
		}
	}
	if !found {
		t.Fatal("missing inventory app")
	}
}
