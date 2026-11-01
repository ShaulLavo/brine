//go:build linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func udpStoreInput(t testing.TB) plan.Input {
	t.Helper()
	in := fixture(t)
	in.Desired.UDP = &policy.UDPDesired{ContainerPort: 4433, Exposure: spec.UDPPublic, Grant: policy.UDPGrant{App: "hello", Exposure: spec.UDPPublic, BindAddress: "192.0.2.10", ClientAddress: "relay.example.test", HostPorts: policy.PortRange{Min: 4433, Max: 4435}}}
	in.State.UDP = &plan.UDPState{Incarnations: []plan.AppIncarnation{}, Reservations: []plan.UDPReservation{}}
	in.Snapshot.LocalAddresses = target.Known([]string{"192.0.2.10"})
	return in
}
func saveUDPOperation(t testing.TB, s *Store, in plan.Input, key string) (Operation, plan.Plan) {
	t.Helper()
	p, err := plan.Build(in)
	if err != nil || p.Kind == plan.Conflict {
		t.Fatalf("UDP plan: %v %v", p.Conflicts, err)
	}
	return saveBuiltUDPOperation(t, s, p, in.Desired, key), p
}
func saveBuiltUDPOperation(t testing.TB, s *Store, p plan.Plan, d policy.Desired, key string) Operation {
	t.Helper()
	if _, err := s.SavePlan(t.Context(), p, d); err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(t.Context(), ops.Intent{Kind: ops.Deploy, PlanID: p.Hash}, "udp-test", key)
	if err != nil {
		t.Fatal(err)
	}
	return op
}
func udpStoredRelease(t testing.TB, op Operation, p plan.Plan, d policy.Desired) Release {
	t.Helper()
	unit, err := quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	return Release{ID: op.ID, PlanID: p.Hash, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: []target.Unit{{Name: unit.Name(), Hash: unit.Hash()}}, CaddyFile: target.CaddyFile{Name: p.App + ".caddy", Hash: "sha256:" + strings.Repeat("e", 64)}, CaddyGeneration: 1}
}
func udpCommittedInput(t testing.TB, s *Store, in plan.Input, r Release) plan.Input {
	t.Helper()
	generation, err := s.Generation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	in.State, err = s.LoadBrineState(t.Context(), in.Snapshot.Identity, generation)
	if err != nil {
		t.Fatal(err)
	}
	in.Snapshot.Generation = target.Known(generation)
	in.Snapshot.Apps = target.Known([]target.App{{Name: "hello", Image: target.Known(target.Image{Digest: r.Image.Digest, Platform: r.Image.Platform}), AllocatedHostPort: target.Known(r.HostPort), QuadletUnits: target.Known(r.Units), Secrets: target.Known([]target.Secret{}), UDPPublication: target.Observation[target.UDPPublication]{Status: target.Absent}}})
	if allocation := in.State.Releases[0].UDP; allocation != nil {
		(*in.Snapshot.Apps.Value)[0].UDPPublication = target.Known(target.UDPPublication{BindAddress: allocation.Endpoint.BindAddress, HostPort: allocation.Endpoint.HostPort, ContainerPort: allocation.ContainerPort, NetworkMode: "pasta"})
	}
	in.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: r.CaddyGeneration, Files: []target.CaddyFile{r.CaddyFile}})
	domains := make([]string, 0, len(in.Desired.Domains))
	for _, domain := range in.Desired.Domains {
		domains = append(domains, string(domain))
	}
	in.Snapshot.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{{Name: "hello.caddy", App: "hello", Domains: target.Known(domains)}})
	return in
}
func requireUDPState(t testing.TB, s *Store, op Operation, p plan.Plan, want string) {
	t.Helper()
	got, err := s.UDPPreparation(t.Context(), op.ID, p)
	if err != nil || got != want {
		t.Fatalf("UDP state %s, want %s: %v", got, want, err)
	}
}
func TestUDPPreparationCommitMoveRevertAndRelease(t *testing.T) {
	s := openTest(t)
	in := udpStoreInput(t)
	op, p := saveUDPOperation(t, s, in, "create")
	r := udpStoredRelease(t, op, p, in.Desired)
	if err := s.CommitRelease(t.Context(), p.App, r); !errors.Is(err, ErrConflict) {
		t.Fatal("unprepared committed", err)
	}
	requireUDPState(t, s, op, p, "unprepared")
	for range 2 {
		if err := s.PrepareUDP(t.Context(), op.ID, p); err != nil {
			t.Fatal(err)
		}
	}
	requireUDPState(t, s, op, p, "prepared")
	for range 2 {
		if err := s.CommitRelease(t.Context(), p.App, r); err != nil {
			t.Fatal(err)
		}
	}
	requireUDPState(t, s, op, p, "committed")
	in = udpCommittedInput(t, s, in, r)
	in.Desired.UDP.Grant.HostPorts = policy.PortRange{Min: 4434, Max: 4435}
	move, next := saveUDPOperation(t, s, in, "move")
	if err := s.PrepareUDP(t.Context(), move.ID, next); err != nil {
		t.Fatal(err)
	}
	requireUDPState(t, s, move, next, "prepared")
	for range 2 {
		if err := s.RevertUDP(t.Context(), move.ID, next); err != nil {
			t.Fatal(err)
		}
	}
	requireUDPState(t, s, move, next, "reverted")
	if err := s.PrepareUDP(t.Context(), move.ID, next); !errors.Is(err, ErrConflict) {
		t.Fatal("reverted claim reused", err)
	}
	// A distinct operation never adopts the first family's released claim.
	in.Desired.Environment[0].Value = "new move"
	move, next = saveUDPOperation(t, s, in, "second move")
	if err := s.PrepareUDP(t.Context(), move.ID, next); err != nil {
		t.Fatal(err)
	}
	r = udpStoredRelease(t, move, next, in.Desired)
	if err := s.CommitRelease(t.Context(), next.App, r); err != nil {
		t.Fatal(err)
	}
	requireUDPState(t, s, move, next, "committed")
	in = udpCommittedInput(t, s, in, r)
	in.Desired.UDP = nil
	release, p := saveUDPOperation(t, s, in, "release UDP")
	if p.UDP == nil || p.UDP.Retire == nil || p.UDP.Next != nil {
		t.Fatal(p.UDP)
	}
	if err := s.PrepareUDP(t.Context(), release.ID, p); err != nil {
		t.Fatal(err)
	}
	r = udpStoredRelease(t, release, p, in.Desired)
	if err := s.CommitRelease(t.Context(), p.App, r); err != nil {
		t.Fatal(err)
	}
	requireUDPState(t, s, release, p, "committed")
	held, err := s.UDPGrantHeld(t.Context(), p.App)
	if err != nil || held {
		t.Fatal(held, err)
	}
}
func TestUDPPrepareSQLiteRaceAndFamilyIsolation(t *testing.T) {
	s := openTest(t)
	in := udpStoreInput(t)
	first, p := saveUDPOperation(t, s, in, "first")
	second, _ := saveUDPOperation(t, s, in, "second")
	other, err := Open(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	var wg sync.WaitGroup
	results := make(chan struct {
		id  string
		err error
	}, 2)
	for i, db := range []*Store{s, other} {
		wg.Add(1)
		go func(i int, db *Store) {
			defer wg.Done()
			id := []string{first.ID, second.ID}[i]
			results <- struct {
				id  string
				err error
			}{id, db.PrepareUDP(t.Context(), id, p)}
		}(i, db)
	}
	wg.Wait()
	close(results)
	winner, loser := "", ""
	for result := range results {
		if result.err == nil {
			winner = result.id
		} else {
			loser = result.id
		}
	}
	if winner == "" || loser == "" {
		t.Fatal("expected one transactional winner")
	}
	if err := s.RevertUDP(t.Context(), loser, p); err == nil {
		t.Fatal("foreign family reverted claim")
	}
	if err := s.SetOperationState(t.Context(), winner, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(t.Context(), winner, ops.Failed); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal abandoned reservation", err)
	}
	if err := s.RevertUDP(t.Context(), winner, p); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(t.Context(), winner, ops.Failed); err != nil {
		t.Fatal(err)
	}
}
func TestUDPResolutionSettlesOnlyItsSourceFamily(t *testing.T) {
	s := openTest(t)
	in := udpStoreInput(t)
	source, p := saveUDPOperation(t, s, in, "source")
	if err := s.PrepareUDP(t.Context(), source.ID, p); err != nil {
		t.Fatal(err)
	}
	for _, state := range []ops.State{ops.Preflight, ops.RecoveryRequired} {
		if err := s.SetOperationState(t.Context(), source.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	successor, _, err := s.CreateOperation(t.Context(), ops.Intent{Kind: ops.Resolve, PlanID: p.Hash, RecoveryOf: source.ID}, "udp-test", "resolve")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareUDP(t.Context(), successor.ID, p); err != nil {
		t.Fatal(err)
	}
	r := udpStoredRelease(t, successor, p, in.Desired)
	if err := s.CommitRelease(t.Context(), p.App, r); err != nil {
		t.Fatal(err)
	}
	requireUDPState(t, s, successor, p, "committed")
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM udp_reservations").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
func TestUDPRemovalSettlementIsAtomic(t *testing.T) {
	s := openTest(t)
	in := udpStoreInput(t)
	op, p := saveUDPOperation(t, s, in, "create")
	if err := s.PrepareUDP(t.Context(), op.ID, p); err != nil {
		t.Fatal(err)
	}
	r := udpStoredRelease(t, op, p, in.Desired)
	if err := s.CommitRelease(t.Context(), p.App, r); err != nil {
		t.Fatal(err)
	}
	in = udpCommittedInput(t, s, in, r)
	p, err := plan.BuildRemove(in)
	if err != nil || p.Kind == plan.Conflict {
		t.Fatal(p.Conflicts, err)
	}
	op = saveBuiltUDPOperation(t, s, p, in.Desired, "remove")
	if err := s.RetireApp(t.Context(), op.ID, p.App, r.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("unprepared retirement", err)
	}
	if err := s.PrepareUDP(t.Context(), op.ID, p); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireApp(t.Context(), op.ID, p.App, "wrong release"); err == nil {
		t.Fatal("wrong release retired")
	}
	for range 2 {
		if err := s.RetireApp(t.Context(), op.ID, p.App, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	requireUDPState(t, s, op, p, "committed")
	if _, err := s.CurrentRelease(t.Context(), p.App); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestUDPPreparationCancellationLeavesNoPartialState(t *testing.T) {
	s := openTest(t)
	in := udpStoreInput(t)
	op, p := saveUDPOperation(t, s, in, "cancel")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.PrepareUDP(ctx, op.ID, p); err == nil {
		t.Fatal("cancelled preparation succeeded")
	}
	requireUDPState(t, s, op, p, "unprepared")
	generation, err := s.Generation(t.Context())
	if err != nil || generation != 0 {
		t.Fatal(fmt.Sprint(generation), err)
	}
}

func TestUDPDirectCommitRejectsStoredIntentWithoutItsReservationPlan(t *testing.T) {
	s := openTest(t)
	in := udpStoreInput(t)
	built, err := plan.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(built)
	if err != nil {
		t.Fatal(err)
	}
	var forged plan.Plan
	if err := json.Unmarshal(raw, &forged); err != nil {
		t.Fatal(err)
	}
	forged.UDP = nil
	forged.Changes = []plan.Change{}
	if _, err := s.SavePlan(t.Context(), forged, in.Desired); err != nil {
		t.Fatal(err)
	}
	r := Release{ID: "forged-release", PlanID: forged.Hash, Image: forged.Image, HostPort: forged.HostPort, Secrets: forged.Secrets, Units: []target.Unit{{Name: "hello.container", Hash: "sha256:" + strings.Repeat("c", 64)}}, CaddyFile: target.CaddyFile{Name: "hello.caddy", Hash: "sha256:" + strings.Repeat("d", 64)}}
	if err := s.CommitRelease(t.Context(), forged.App, r); !errors.Is(err, plan.ErrInvalidUDPExecution) {
		t.Fatal("UDP desired committed as HTTP-only", err)
	}
	if _, err := s.CurrentRelease(t.Context(), forged.App); !errors.Is(err, ErrNotFound) {
		t.Fatal("invalid intent changed live head", err)
	}
}
