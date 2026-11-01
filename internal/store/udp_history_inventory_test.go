//go:build linux

package store

import (
	"fmt"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

type unpublishedUDPCase struct {
	name       string
	rows       int
	socket     bool
	secret     bool
	wantAbsent bool
}

func TestReleasedUDPClaimWithoutReleaseCannotProveAbsence(t *testing.T) {
	for _, tc := range []unpublishedUDPCase{
		{"one remnant", 1, true, true, false},
		{"overflow with remnant", 65, true, true, false},
		{"overflow without socket", 65, false, true, false},
		{"history-only discovery", 1, true, false, false},
		{"settled empty runtime", 1, false, true, true},
		{"settled history-only empty runtime", 1, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) { checkReleasedUDPClaimAbsence(t, tc) })
	}
}

func checkReleasedUDPClaimAbsence(t *testing.T, tc unpublishedUDPCase) {
	t.Helper()
	s := openTest(t)
	op := operation(t, s)
	for i := 0; i < tc.rows; i++ {
		id := fmt.Sprintf("udp-unpublished-%d", i)
		if err := insertUDPFixture(t, s, id, "hello", op, 4433); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("UPDATE udp_reservations SET state='released',owner=NULL,released_by=? WHERE id=?", op, id); err != nil {
			t.Fatal(err)
		}
	}
	collector, runner, _, _, _ := inventoryLifecycleFixture(t, s)
	if tc.socket {
		runner["ss -H -lunpe"] = "UNCONN 0 0 192.0.2.10:4433 0.0.0.0:*"
	}
	if !tc.secret {
		runner["podman --remote=false secret ls --format {{.ID}} {{.Name}}"] = ""
	}
	snapshot, err := collector.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, app := range *snapshot.Apps.Value {
		if app.Name != "hello" {
			continue
		}
		found = true
		if app.RuntimeAbsent() != tc.wantAbsent {
			t.Fatalf("released history/runtime proof: absent=%v want=%v", app.RuntimeAbsent(), tc.wantAbsent)
		}
	}
	if !found {
		t.Fatal("released-only history must discover its app without a release or secret")
	}
	assertUnpublishedUDPHistory(t, s, tc.rows)
}

func assertUnpublishedUDPHistory(t *testing.T, s *Store, count int) {
	t.Helper()
	control, err := s.InventoryState(t.Context())
	if err != nil || len(control.Apps) != 1 || len(control.UDPReservations) != 0 {
		t.Fatal("history must remain separate from live reservations", control, err)
	}
	app := control.Apps[0]
	want := target.KnownStatus
	if count > maxUDPHistory {
		want = target.Unknown
	}
	if app.RetiredUDP == nil || app.RetiredUDP.Status != want {
		t.Fatal("missing or truncated history lost its observation", app.RetiredUDP)
	}
}

func TestUnpublishedLiveUDPClaimDoesNotProveRuntimeAbsence(t *testing.T) {
	s := openTest(t)
	if err := insertUDPFixture(t, s, "udp-unsettled", "hello", operation(t, s), 4433); err != nil {
		t.Fatal(err)
	}
	collector, _, _, _, _ := inventoryLifecycleFixture(t, s)
	snapshot, err := collector.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range *snapshot.Apps.Value {
		if app.Name == "hello" && app.RuntimeAbsent() {
			t.Fatal("unsettled unpublished claim became retired runtime")
		}
	}
}
