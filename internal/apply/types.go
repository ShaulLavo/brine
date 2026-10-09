package apply

import (
	"context"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/target"
)

type State = ops.State
type Operation = ops.Operation
type Event = ops.Event
type Release = ops.Release

const (
	Queued           = ops.Queued
	LaunchUnknown    = ops.LaunchUnknown
	Preflight        = ops.Preflight
	Preparing        = ops.Preparing
	Quiescing        = ops.Quiescing
	Starting         = ops.Starting
	Checking         = ops.Checking
	Committing       = ops.Committing
	RollingBack      = ops.RollingBack
	Succeeded        = ops.Succeeded
	Failed           = ops.Failed
	RolledBack       = ops.RolledBack
	RecoveryRequired = ops.RecoveryRequired
)

type Journal interface {
	GetOperation(context.Context, string) (ops.Operation, error)
	AppendEvent(context.Context, string, Event) (uint64, error)
	SetOperationState(context.Context, string, State) error
}
type ReleaseStore interface {
	CurrentRelease(context.Context, string) (Release, bool, error)
	CommitRelease(context.Context, string, Release) error
}
type PlanLoader interface {
	LoadPlan(context.Context, string) (plan.Plan, policy.Desired, error)
}

// FactsReader must collect fresh inventory, current policy-normalized desired
// input, verified registry metadata and committed releases under the host lock.
// Routing.Sites is reconstructed from those committed inputs, never live text.
type Facts struct {
	Input   plan.Input
	Routing caddy.State
}
type FactsReader interface {
	Read(context.Context) (Facts, error)
}
type Units interface {
	Stage(context.Context, quadlet.Unit) error
	Install(context.Context, quadlet.Unit, string) error
	Rollback(context.Context, string, string, string) error
}
type Routes interface {
	Publish(context.Context, caddy.State, plan.Plan, policy.Desired) (caddy.Result, error)
	Restore(context.Context, caddy.State, caddy.State) error
}
type Health interface {
	Check(context.Context, policy.Desired, target.Port, bool) error
}

// Compatibility must affirm that starting the candidate cannot make existing
// data unreadable by the previous release. Absence or uncertainty fails closed.
type Compatibility interface {
	Safe(context.Context, policy.Desired, policy.Desired) (bool, error)
}
type stepPayload = ops.StepPayload

type Error struct {
	Step  string
	Code  string
	State State
	Cause error
}

func (e *Error) Error() string {
	return "apply: " + e.Step + " (" + e.Code + ", " + string(e.State) + ")"
}
func (e *Error) Unwrap() error { return e.Cause }
