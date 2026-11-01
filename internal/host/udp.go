package host

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type udpProofCollector interface {
	UDPReady(context.Context, string, string, plan.UDPAllocation, []target.Unit) error
	UDPAbsent(context.Context, string, plan.UDPEndpoint) error
}
type udpProof struct {
	store     *store.Store
	inventory udpProofCollector
}

func (u udpProof) intent(ctx context.Context, id string, p plan.Plan, compensation bool) (plan.Plan, policy.Desired, error) {
	stored, d, err := u.store.LoadPlan(ctx, p.Hash)
	if err != nil {
		return stored, d, err
	}
	if !reflect.DeepEqual(stored, p) || p.UDP == nil {
		return stored, d, errors.New("UDP immutable plan differs")
	}
	state, err := u.store.UDPPreparation(ctx, id, p)
	if err != nil {
		return stored, d, err
	}
	if udpClaimed(p) && state != "prepared" && !(compensation && (state == "reverted" || state == "unprepared" && p.UDP.Next == nil && p.UDP.Retire != nil)) {
		return stored, d, errors.New("UDP operation preparation differs")
	}
	if !udpClaimed(p) && state != "unprepared" {
		return stored, d, errors.New("UDP retained reservation differs")
	}
	return stored, d, nil
}
func udpClaimed(p plan.Plan) bool {
	return p.UDP.Retire != nil || p.UDP.Next != nil && p.UDP.Keep == ""
}
func (u udpProof) Ready(ctx context.Context, id string, p plan.Plan, previous bool) error {
	_, d, err := u.intent(ctx, id, p, previous)
	if err != nil {
		return err
	}
	allocation := p.UDP.Next
	planID := p.Hash
	var units []target.Unit
	if previous || p.Lifecycle != "" {
		r, err := u.store.CurrentRelease(ctx, p.App)
		if err != nil {
			return err
		}
		old, _, err := u.store.LoadPlan(ctx, r.PlanID)
		if err != nil || old.UDP == nil || old.UDP.Next == nil {
			return errors.New("UDP previous allocation unavailable")
		}
		if !previous && !reflect.DeepEqual(old.UDP.Next, p.UDP.Next) {
			return errors.New("UDP lifecycle allocation differs")
		}
		allocation, units, planID = old.UDP.Next, r.Units, old.Hash
	} else {
		if p.Image.ManifestDigest.Value == nil {
			return errors.New("UDP image unverified")
		}
		unit, err := quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
		if err != nil {
			return err
		}
		units = []target.Unit{{Name: unit.Name(), Hash: unit.Hash()}}
		r, readErr := u.store.CurrentRelease(ctx, p.App)
		if readErr != nil && !errors.Is(readErr, store.ErrNotFound) {
			return readErr
		}
		units = udpProofUnits(units, r.Units, unit.Name())
	}
	if allocation == nil || u.inventory == nil {
		return errors.New("UDP proof unavailable")
	}
	return probeUDP(ctx, func(ctx context.Context) error { return u.inventory.UDPReady(ctx, p.App, planID, *allocation, units) })
}
func (u udpProof) Absent(ctx context.Context, id string, p plan.Plan, endpoint plan.UDPEndpoint) error {
	if _, _, err := u.intent(ctx, id, p, true); err != nil {
		return err
	}
	if p.UDP.Retire != nil && p.UDP.Retire.Endpoint == endpoint || p.UDP.Next != nil && p.UDP.Keep == "" && p.UDP.Next.Endpoint == endpoint {
		return probeUDP(ctx, func(ctx context.Context) error { return u.inventory.UDPAbsent(ctx, p.App, endpoint) })
	}
	return errors.New("UDP absence endpoint outside operation")
}

func (f operationFacts) udpRebuild(ctx context.Context, factsInput plan.Input) (plan.Input, error) {
	if f.recoveryPlan.UDP == nil {
		return factsInput, nil
	}
	family, err := f.service.Store.UDPOperationFamily(ctx, f.recoveryOperation.ID, f.recoveryPlan)
	if err != nil {
		return factsInput, err
	}
	_, prepared, err := plan.UnapplyUDP(factsInput.State, f.recoveryPlan, family)
	if err != nil || !prepared {
		return factsInput, err
	}
	collector, ok := f.service.Inventory.(interface {
		RebuildUDP(context.Context, plan.Plan, []string) (target.Snapshot, error)
	})
	if !ok {
		return factsInput, errors.New("UDP rebuild collector unavailable")
	}
	snapshot, err := collector.RebuildUDP(ctx, f.recoveryPlan, family)
	if err == nil {
		factsInput.UDPRebuildSnapshot = &snapshot
	}
	return factsInput, err
}

func probeUDP(ctx context.Context, probe func(context.Context) error) error {
	for {
		err := probe(ctx)
		if err == nil {
			return nil
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
}

func udpProofUnits(units, previous []target.Unit, name string) []target.Unit {
	for _, old := range previous {
		if old.Name != name {
			units = append(units, old)
		}
	}
	return units
}
