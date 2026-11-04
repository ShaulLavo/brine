//go:build linux

package store

import (
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

func operationInventoryApp(t *testing.T, state target.ControlInventory) target.ControlApp {
	t.Helper()
	for _, app := range state.Apps {
		if app.Name == "hello" {
			return app
		}
	}
	t.Fatal("hello missing from control", state.Apps)
	return target.ControlApp{}
}

// requireHead reports whether a read attaches hello's committed UDP head as Known.
func requireHead(t *testing.T, state target.ControlInventory, err error, want bool, create plan.Plan) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	app := operationInventoryApp(t, state)
	attached := app.Status == target.KnownStatus && app.UDP != nil && app.UDP.PlanID == create.Hash && app.PortReservation != nil
	if attached != want {
		t.Fatalf("committed head attached=%v, want %v: %+v", attached, want, app)
	}
}

func TestOperationInventoryStateExemptsOnlyTheObservingFamily(t *testing.T) {
	s := openTest(t)
	in := udpStoreInput(t)
	created, create := saveUDPOperation(t, s, in, "create")
	if err := s.PrepareUDP(t.Context(), created.ID, create); err != nil {
		t.Fatal(err)
	}
	head := udpStoredRelease(t, created, create, in.Desired)
	if err := s.CommitRelease(t.Context(), create.App, head); err != nil {
		t.Fatal(err)
	}
	advanceRemoval(t, s, created.ID)
	if err := s.SetOperationState(t.Context(), created.ID, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	state, err := s.InventoryState(t.Context())
	requireHead(t, state, err, true, create)
	in = udpCommittedInput(t, s, in, head)
	removal, err := plan.BuildRemove(in)
	if err != nil || removal.Kind != plan.Update {
		t.Fatal(removal.Conflicts, err)
	}
	removing := saveBuiltUDPOperation(t, s, removal, in.Desired, "remove")

	state, err = s.InventoryState(t.Context())
	requireHead(t, state, err, false, create)
	if app := operationInventoryApp(t, state); app.Status != target.Unknown {
		t.Fatal("ordinary read lost the unfinished removal", app)
	}
	state, err = s.OperationInventoryState(t.Context(), removing.ID, removal.Hash)
	requireHead(t, state, err, true, create)

	for _, tc := range []struct{ name, id, plan string }{
		{"empty observer", "", removal.Hash},
		{"empty plan", removing.ID, ""},
		{"unknown operation", "01unknownoperation", removal.Hash},
		{"another plan", removing.ID, create.Hash},
		{"finished operation of the head", created.ID, removal.Hash},
	} {
		if _, err := s.OperationInventoryState(t.Context(), tc.id, tc.plan); err == nil {
			t.Fatal(tc.name, "was accepted as the observing operation")
		}
	}

	// The same plan accepted twice is still a concurrent, foreign mutation.
	twin, _, err := s.CreateOperation(t.Context(), ops.Intent{Kind: ops.Deploy, PlanID: removal.Hash}, "udp-test", "remove-twin")
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.OperationInventoryState(t.Context(), removing.ID, removal.Hash)
	requireHead(t, state, err, false, create)
	if err = s.SetOperationState(t.Context(), twin.ID, ops.Failed); err != nil {
		t.Fatal(err)
	}
	global, _, err := s.CreateOperation(t.Context(), ops.Intent{Kind: ops.Reconcile}, "udp-test", "global")
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.OperationInventoryState(t.Context(), removing.ID, removal.Hash)
	requireHead(t, state, err, false, create)
	if err = s.SetOperationState(t.Context(), global.ID, ops.Failed); err != nil {
		t.Fatal(err)
	}

	// A resolution observes for its whole recovery chain, never for a sibling.
	for _, state := range []ops.State{ops.Preflight, ops.RecoveryRequired} {
		if err = s.SetOperationState(t.Context(), removing.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	resolving, _, err := s.CreateOperation(t.Context(), ops.Intent{Kind: ops.Resolve, RecoveryOf: removing.ID, PlanID: removal.Hash}, "udp-test", "resolve")
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.OperationInventoryState(t.Context(), resolving.ID, removal.Hash)
	requireHead(t, state, err, true, create)
	state, err = s.InventoryState(t.Context())
	requireHead(t, state, err, false, create)
}
