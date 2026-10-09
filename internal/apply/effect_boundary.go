package apply

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/plan"
)

// Unlike an uncertain subprocess outcome, this refusal proves the requested
// mutation was not attempted. Never let effect readback turn it into success.
type boundaryRefusal struct{ cause error }

func (e *boundaryRefusal) Error() string { return "apply: unsafe effect boundary" }
func (e *boundaryRefusal) Unwrap() error { return e.cause }

// Called after durable intent and immediately before the shared effect path.
// A host lock excludes other Brine jobs, not systemd restart jobs or file changes.
func (x *execution) verifyEffectBoundary(ctx context.Context, step string) error {
	name := x.plan.App + ".container"
	previous := x.previousUnitHash()
	candidate := previous
	if x.unit.Name() != "" {
		name = x.unit.Name()
		candidate = x.unit.Hash()
	}
	hashes := []string{candidate, previous}
	stopped := true
	switch step {
	case "quiesce_old":
		if !x.hasPrevious {
			return nil
		}
		hashes = []string{previous}
		stopped = false
	case "stop", "rollback_quiesce":
		stopped = false
		// Absence is safe for unit removal, never proof of a running writer identity.
		if previous == "" {
			hashes = []string{candidate}
		}
	case "stop_unit":
		hashes = []string{previous}
		stopped = false
	case "install_unit":
		hashes = []string{previous}
	case "start_unit":
		hashes = []string{candidate}
		if x.plan.Lifecycle != "" {
			hashes = []string{previous}
			stopped = false
		}
	case "rollback_unit", "rollback_route":
	case "rollback_reload", "rollback_start":
		hashes = []string{previous}
		if step == "rollback_start" && !x.installed && !x.started {
			stopped = false
		}
	case "reload_units":
		hashes = []string{candidate}
		if x.plan.Lifecycle == plan.RemoveApp {
			hashes = []string{""}
		} else if x.plan.Lifecycle != "" {
			hashes = []string{previous}
		}
	case "remove_unit":
		hashes = []string{previous, ""}
	default:
		return nil
	}
	refuse := func(err error) error {
		return &Error{Step: step, Code: "rollback_failed", Cause: &boundaryRefusal{cause: err}}
	}
	if x.executor.Units == nil {
		return refuse(errors.New("live ownership reader unavailable"))
	}
	// Bracket fresh ownership with independent job/unit/container observations.
	// Recheck ownership after waiting, since settlement itself may take time.
	if err := x.executor.Units.VerifyCurrent(ctx, name, hashes...); err != nil {
		return refuse(err)
	}
	writer := x.waitWriter(ctx)
	if writer != writerStopped && writer != writerRunning || stopped && writer != writerStopped {
		return refuse(errors.New("writer or manager queue not settled"))
	}
	if err := x.executor.Units.VerifyCurrent(ctx, name, hashes...); err != nil {
		return refuse(err)
	}
	writer = x.inspectWriter(ctx)
	if writer != writerStopped && writer != writerRunning || stopped && writer != writerStopped {
		return refuse(errors.New("writer or manager queue changed"))
	}
	if err := x.executor.Units.VerifyCurrent(ctx, name, hashes...); err != nil {
		return refuse(err)
	}
	return nil
}
