//go:build linux

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

func insertUDPFixture(t *testing.T, s *Store, id, app, op string, port int) error {
	t.Helper()
	r := plan.UDPReservation{ID: id, App: app, Incarnation: "sha256:" + strings.Repeat("a", 64), State: "staged", Owner: op, Endpoint: plan.UDPEndpoint{Family: "ipv4", BindAddress: "192.0.2.10", HostPort: 4433}}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec("INSERT INTO udp_reservations(id,app,incarnation,family,bind_address,host_port,state,owner,created_by,canonical) VALUES(?,?,?,?,?,?,'staged',?,?,?)", id, app, r.Incarnation, r.Endpoint.Family, r.Endpoint.BindAddress, port, op, op, raw)
	return err
}
func TestUDPRealSQLiteReservationInvariants(t *testing.T) {
	s := openTest(t)
	op := operation(t, s)
	generation, err := s.Generation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := insertUDPFixture(t, s, "udp-first", "hello", op, 4433); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"DELETE FROM udp_reservations WHERE id='udp-first'",
		"UPDATE udp_reservations SET host_port=4434 WHERE id='udp-first'",
		"UPDATE udp_reservations SET incarnation='different' WHERE id='udp-first'",
		"UPDATE udp_reservations SET canonical='{}' WHERE id='udp-first'",
		"UPDATE udp_reservations SET state='releasing' WHERE id='udp-first'",
		"UPDATE udp_reservations SET state='active' WHERE id='udp-first'",
	} {
		if _, err := s.db.Exec(query); err == nil {
			t.Fatalf("accepted illegal reservation mutation: %s", query)
		}
	}
	if err := insertUDPFixture(t, s, "udp-other", "other", op, 4433); err == nil {
		t.Fatal("duplicate endpoint accepted")
	}
	if err := insertUDPFixture(t, s, "udp-extra", "hello", op, 4434); err == nil {
		t.Fatal("second staged claim accepted")
	}
	for _, query := range []string{
		"UPDATE udp_reservations SET state='active',owner=NULL WHERE id='udp-first'",
		"UPDATE udp_reservations SET state='releasing',owner='" + op + "' WHERE id='udp-first'",
		"UPDATE udp_reservations SET state='active',owner=NULL WHERE id='udp-first'",
		"UPDATE udp_reservations SET state='releasing',owner='" + op + "' WHERE id='udp-first'",
		"UPDATE udp_reservations SET state='released',owner=NULL,released_by='" + op + "' WHERE id='udp-first'",
	} {
		if _, err := s.db.Exec(query); err != nil {
			t.Fatal(query, err)
		}
	}
	if _, err := s.db.Exec("UPDATE udp_reservations SET state='active',released_by=NULL WHERE id='udp-first'"); err == nil {
		t.Fatal("released row resurrected")
	}
	if err := insertUDPFixture(t, s, "udp-new", "other", op, 4433); err != nil {
		t.Fatal("released endpoint not reusable", err)
	}
	actual, err := s.Generation(t.Context())
	if err != nil || actual != generation {
		t.Fatal("reservations advanced control generation", actual, generation, err)
	}
	inventory, err := s.InventoryState(t.Context())
	if err != nil || len(inventory.UDPReservations) != 1 || inventory.UDPReservations[0].ID != "udp-new" {
		t.Fatal(inventory.UDPReservations, err)
	}
}
func TestUDPRealSQLiteEndpointRace(t *testing.T) {
	a := openTest(t)
	op := operation(t, a)
	b, err := Open(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, s := range []*Store{a, b} {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			results <- insertUDPFixture(t, s, []string{"udp-a", "udp-b"}[i], []string{"first", "second"}[i], op, 4433)
		}(i, s)
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatal("endpoint race winners", wins)
	}
}
func TestUDPIncarnationFoundedKeptAndReadBack(t *testing.T) {
	s := openTest(t)
	id, in := saved(t, s)
	r := release(t, s, id)
	if err := s.CommitRelease(t.Context(), "hello", r); err != nil {
		t.Fatal(err)
	}
	before, err := s.LoadBrineState(t.Context(), in.Snapshot.Identity, 1)
	if err != nil || len(before.UDP.Incarnations) != 1 || before.UDP.Incarnations[0].ID != id {
		t.Fatal(before.UDP, err)
	}
	r.ID = "second-release"
	if err := s.CommitRelease(t.Context(), "hello", r); err != nil {
		t.Fatal(err)
	}
	after, err := s.LoadBrineState(t.Context(), in.Snapshot.Identity, 2)
	if err != nil || after.UDP.Incarnations[0] != before.UDP.Incarnations[0] {
		t.Fatal(after.UDP, err)
	}
	if _, err := s.db.Exec("UPDATE release_heads SET incarnation_id=? WHERE app='hello'", "sha256:"+strings.Repeat("b", 64)); err == nil {
		t.Fatal("head incarnation mutable")
	}
}
func TestUDPMigrationBackfillsExistingHead(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE schema_version(version INTEGER);INSERT INTO schema_version VALUES(33);
CREATE TABLE plans(id TEXT PRIMARY KEY);CREATE TABLE operations(id TEXT PRIMARY KEY);
CREATE TABLE releases(app TEXT,id TEXT,plan_id TEXT,PRIMARY KEY(app,id));
CREATE TABLE release_heads(app TEXT PRIMARY KEY,current_id TEXT NOT NULL,previous_id TEXT);
INSERT INTO plans VALUES('founding-plan');INSERT INTO releases VALUES('hello','release','founding-plan');INSERT INTO release_heads VALUES('hello','release',NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = migrateUDPReservations(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var incarnation string
	if err = db.QueryRow("SELECT incarnation_id FROM release_heads WHERE app='hello'").Scan(&incarnation); err != nil || incarnation != "founding-plan" {
		t.Fatal(incarnation, err)
	}
}

func TestUDPFoundingIdentityChangesOnlyAfterHeadRetirement(t *testing.T) {
	s := openTest(t)
	in := fixture(t)
	var first string
	for i, version := range []string{"first", "update", "recreated"} {
		in.Desired.Environment[0].Value = version
		in.State.Generation = uint64(i)
		in.Snapshot.Generation = target.Known(uint64(i))
		p, err := plan.Build(in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.SavePlan(t.Context(), p, in.Desired); err != nil {
			t.Fatal(err)
		}
		r := release(t, s, version)
		r.PlanID = p.Hash
		if err = s.CommitRelease(t.Context(), "hello", r); err != nil {
			t.Fatal(err)
		}
		var incarnation string
		if err = s.db.QueryRow("SELECT incarnation_id FROM release_heads WHERE app='hello'").Scan(&incarnation); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = incarnation
		}
		if i == 1 && incarnation != first {
			t.Fatal("update refounded identity")
		}
		if i == 2 && (incarnation == first || incarnation != p.Hash) {
			t.Fatal("recreated app inherited old incarnation")
		}
		if i == 1 {
			if _, err = s.db.Exec("DELETE FROM release_heads WHERE app='hello'"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestUDPReservationPersistsAndDirectCommitCannotBypassGate(t *testing.T) {
	s := openTest(t)
	op := operation(t, s)
	if err := insertUDPFixture(t, s, "udp-persisted", "hello", op, 4433); err != nil {
		t.Fatal(err)
	}
	other, err := OpenReadOnly(t.Context(), s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	state, err := other.LoadBrineState(t.Context(), fixture(t).Snapshot.Identity, 0)
	if err != nil || len(state.UDP.Reservations) != 1 || state.UDP.Reservations[0].ID != "udp-persisted" {
		t.Fatal(state.UDP, err)
	}
	r := release(t, s, "forged-http-only-commit")
	if err := s.CommitRelease(t.Context(), "hello", r); !errors.Is(err, plan.ErrUDPExecutionUnavailable) {
		t.Fatal("HTTP-only commit abandoned a UDP reservation", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM releases").Scan(&count); err != nil || count != 0 {
		t.Fatal("refused commit changed release history", count, err)
	}
}
