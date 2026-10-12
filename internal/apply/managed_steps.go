package apply

import (
	"context"
	"github.com/ShaulLavo/brine/internal/plan"
)

func managedFrontendStep(step string) string {
	switch step {
	case "frontend_pull":
		return "pull_image"
	case "frontend_verify":
		return "verify_image"
	case "frontend_secrets":
		return "ensure_secrets"
	case "frontend_stage":
		return "stage_unit"
	case "managed_frontend_stop":
		return "stop_unit"
	case "managed_frontend_start":
		return "start_unit"
	case "frontend_stop":
		return "quiesce_old"
	case "frontend_install":
		return "install_unit"
	case "frontend_reload":
		return "reload_units"
	case "frontend_start":
		return "start_unit"
	case "frontend_check":
		return "check_direct"
	case "managed_route":
		return "publish_route"
	case "managed_route_check":
		return "check_routed"
	case "managed_commit":
		return "commit"
	default:
		return step
	}
}

func (x *execution) reconcileManagedUnknown(ctx context.Context, step string) resolution {
	if x.executor.Managed == nil || x.plan.Managed == nil {
		return unresolved
	}
	switch step {
	case "managed_engine_stop":
		if x.executor.Managed.EngineStopped(ctx, x.plan) == nil {
			return applied
		}
		return unresolved
	case "engine_start", "engine_check", "managed_engine_start", "managed_engine_check":
		if x.executor.Managed.EngineReady(ctx, x.plan) == nil {
			return applied
		}
		if x.executor.Managed.BeforeEngineStart(ctx, x.plan) == nil {
			return notApplied
		}
		return unresolved
	case "engine_install":
		if x.executor.Managed.VerifyEngineUnit(ctx, x.plan) == nil {
			return applied
		}
		return unresolved
	case "engine_stage", "engine_reload", "frontend_bind":
		return unresolved
	default:
		translated := managedFrontendStep(step)
		if translated == step {
			return unresolved
		}
		return x.reconcileUnknown(ctx, translated)
	}
}

func managedHealthStep(p plan.Plan, step string) bool {
	return p.Managed != nil && (step == "frontend_check" || step == "managed_route_check" || step == "managed_frontend_check")
}
