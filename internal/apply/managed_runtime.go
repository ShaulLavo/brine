package apply

import (
	"context"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

// ManagedRuntime owns the binding service and separate frontend/route permits.
// It cannot initialize/publish Wasm or restore engine state from an app release.
type ManagedRuntime interface {
	StageEngine(context.Context, plan.Plan, ops.Release) error
	InstallEngine(context.Context, plan.Plan, ops.Release) error
	VerifyEngineUnit(context.Context, plan.Plan) error
	EngineReady(context.Context, plan.Plan) error
	BeforeEngineStart(context.Context, plan.Plan) error
	BindFrontend(context.Context, string, plan.Plan, string) error
	RestoreFrontendPermit(context.Context, string, string) error
	BeforeFrontendStart(context.Context, string, plan.Plan, policy.Desired, string) error
	BeforeIngress(context.Context, string, plan.Plan, policy.Desired) error
}
