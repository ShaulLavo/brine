package apply

import (
	"context"
	"errors"
	"slices"

	"github.com/ShaulLavo/brine/internal/plan"
)

type UDPReservations interface {
	PrepareUDP(context.Context, string, plan.Plan) error
	UDPPreparation(context.Context, string, plan.Plan) (string, error)
	RevertUDP(context.Context, string, plan.Plan) error
}
type UDPChecks interface {
	Ready(context.Context, string, plan.Plan, bool) error
	Absent(context.Context, string, plan.Plan, plan.UDPEndpoint) error
}

func (x *execution) udpFamily() []string { return append([]string{x.id}, x.writerStartOwners...) }
func udpPreparationNeeded(p plan.Plan) bool {
	return p.UDP != nil && (p.UDP.Retire != nil || p.UDP.Next != nil && p.UDP.Keep == "")
}
func (x *execution) prepareUDP(ctx context.Context) error {
	if x.executor.UDPReservations == nil {
		return errors.New("UDP reservation adapter required")
	}
	return x.executor.UDPReservations.PrepareUDP(ctx, x.id, x.plan)
}
func (x *execution) revertUDP(ctx context.Context) error {
	if x.executor.UDPReservations == nil {
		return errors.New("UDP reservation adapter required")
	}
	return x.executor.UDPReservations.RevertUDP(ctx, x.id, x.plan)
}
func (x *execution) checkUDP(ctx context.Context) error {
	if x.executor.UDPChecks == nil {
		return errors.New("UDP proof adapter required")
	}
	return x.executor.UDPChecks.Ready(ctx, x.id, x.plan, false)
}
func (x *execution) checkUDPRetired(ctx context.Context) error {
	if x.executor.UDPChecks == nil || x.plan.UDP == nil || x.plan.UDP.Retire == nil {
		return errors.New("UDP retirement proof required")
	}
	return x.executor.UDPChecks.Absent(ctx, x.id, x.plan, x.plan.UDP.Retire.Endpoint)
}
func (x *execution) rollbackUDPCheck(ctx context.Context) error {
	if x.executor.UDPChecks == nil {
		return errors.New("UDP compensation proof required")
	}
	if x.hasPrevious && x.previousDesired.UDP != nil {
		if err := x.executor.UDPChecks.Ready(ctx, x.id, x.plan, true); err != nil {
			return err
		}
	}
	if x.plan.UDP.Next != nil && x.plan.UDP.Keep == "" {
		return x.executor.UDPChecks.Absent(ctx, x.id, x.plan, x.plan.UDP.Next.Endpoint)
	}
	return nil
}
func (x *execution) udpResolution(ctx context.Context, step string) resolution {
	if x.executor.UDPReservations == nil {
		return unresolved
	}
	state, err := x.executor.UDPReservations.UDPPreparation(ctx, x.id, x.plan)
	if err != nil {
		return unresolved
	}
	switch step {
	case "prepare_udp":
		if state == "prepared" {
			return applied
		}
		if state == "unprepared" {
			return notApplied
		}
	case "revert_udp", "rollback_udp":
		if state == "reverted" || state == "unprepared" {
			return applied
		}
		if state == "prepared" {
			return notApplied
		}
	case "commit", "retire_app":
		if state == "committed" {
			return applied
		}
	}
	return unresolved
}
func (x *execution) udpSettled(ctx context.Context) error {
	if x.plan.UDP == nil || !udpPreparationNeeded(x.plan) {
		return nil
	}
	if x.executor.UDPReservations == nil {
		return errors.New("UDP reservation state unavailable")
	}
	state, err := x.executor.UDPReservations.UDPPreparation(ctx, x.id, x.plan)
	if err != nil {
		return err
	}
	if !slices.Contains([]string{"unprepared", "committed", "reverted"}, state) {
		return errors.New("UDP reservation unsettled")
	}
	return nil
}

func udpReadOnly(name string) bool {
	return name == "check_udp" || name == "check_udp_retired" || name == "rollback_udp_check"
}

func (x *execution) revertUnstartedUDP(ctx context.Context) error {
	if !udpPreparationNeeded(x.plan) || !x.udpPreparationIntent || x.recoveryRollback["revert_udp"] {
		return nil
	}
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), x.executor.effectTimeout())
	defer cancel()
	return x.step(probe, "revert_udp", Preparing, "udp_reservation_failed", x.revertUDP)
}
func (x *execution) compensateUDP(step func(string, string, func(context.Context) error) error) error {
	if x.plan.UDP == nil {
		return nil
	}
	if err := step("rollback_udp_check", "udp_unready", x.rollbackUDPCheck); err != nil {
		return err
	}
	if udpPreparationNeeded(x.plan) {
		return step("rollback_udp", "udp_reservation_failed", x.revertUDP)
	}
	return nil
}
