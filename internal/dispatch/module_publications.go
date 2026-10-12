package dispatch

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type ModulePlanArgs struct {
	App                    string   `json:"app"`
	PreparedPointID        string   `json:"prepared_point_id"`
	TargetDeploymentPlanID string   `json:"target_deployment_plan_id"`
	Wasm                   []byte   `json:"wasm"`
	RecoveryReleaseIDs     []string `json:"recovery_release_ids"`
}

func (a ModulePlanArgs) Valid() bool {
	if !ValidApp(a.App) || !data.ValidID(a.PreparedPointID) || !validModulePlanID(a.TargetDeploymentPlanID) || len(a.Wasm) == 0 || len(a.Wasm) > datapublish.ModuleArtifactLimit || a.RecoveryReleaseIDs == nil || len(a.RecoveryReleaseIDs) > 16 || !slices.IsSorted(a.RecoveryReleaseIDs) {
		return false
	}
	previous := ""
	for _, id := range a.RecoveryReleaseIDs {
		if !jobs.ValidID(id) || id == previous {
			return false
		}
		previous = id
	}
	return true
}

func validModulePlanID(id string) bool {
	return len(id) == 71 && id[:7] == "sha256:" && data.ValidCatalogHash(id[7:])
}

type ModuleApplyArgs struct {
	App            string `json:"app"`
	PlanID         string `json:"plan_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (a ModuleApplyArgs) Valid() bool {
	return ValidApp(a.App) && validModulePlanID(a.PlanID) && data.ValidID(a.IdempotencyKey)
}

type ModuleInspectArgs struct {
	PlanID string `json:"plan_id"`
}

func (a ModuleInspectArgs) Valid() bool { return validModulePlanID(a.PlanID) }

type ModuleResolveArgs struct {
	App                 string `json:"app"`
	PlanID              string `json:"plan_id"`
	OriginalOperationID string `json:"original_operation_id"`
	IdempotencyKey      string `json:"idempotency_key"`
}

func (a ModuleResolveArgs) Valid() bool {
	return ValidApp(a.App) && validModulePlanID(a.PlanID) && jobs.ValidID(a.OriginalOperationID) && data.ValidID(a.IdempotencyKey)
}

type ModulePublicationOperations interface {
	Plan(context.Context, ModulePlanArgs) (datapublish.ReviewView, error)
	Apply(context.Context, ModuleApplyArgs) (jobs.Accepted, error)
	Inspect(context.Context, ModuleInspectArgs) (datapublish.Inspection, error)
	Resolve(context.Context, ModuleResolveArgs) (jobs.Accepted, error)
}

func decodeModulePlan(raw json.RawMessage) (any, error) {
	var args ModulePlanArgs
	fields := []string{"app", "prepared_point_id", "target_deployment_plan_id", "wasm", "recovery_release_ids"}
	if _, err := strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, &args) != nil || !args.Valid() {
		return nil, strictjson.ErrObject
	}
	return args, nil
}
func decodeModuleApply(raw json.RawMessage) (any, error) {
	var args ModuleApplyArgs
	if _, err := strictjson.Object(raw, "app", "plan_id", "idempotency_key"); err != nil || json.Unmarshal(raw, &args) != nil || !args.Valid() {
		return nil, strictjson.ErrObject
	}
	return args, nil
}
func decodeModuleInspect(raw json.RawMessage) (any, error) {
	var args ModuleInspectArgs
	if _, err := strictjson.Object(raw, "plan_id"); err != nil || json.Unmarshal(raw, &args) != nil || !args.Valid() {
		return nil, strictjson.ErrObject
	}
	return args, nil
}
func decodeModuleResolve(raw json.RawMessage) (any, error) {
	var args ModuleResolveArgs
	if _, err := strictjson.Object(raw, "app", "plan_id", "original_operation_id", "idempotency_key"); err != nil || json.Unmarshal(raw, &args) != nil || !args.Valid() {
		return nil, strictjson.ErrObject
	}
	return args, nil
}
func (s *Server) plannedNativeDataOperation(ctx context.Context, input any) (any, error) {
	switch input.(type) {
	case ModulePlanArgs, ModuleApplyArgs, ModuleInspectArgs, ModuleResolveArgs:
		if s.ModulePublications == nil {
			return nil, result.New(result.DependencyMissing, nil)
		}
	case NativeRetirePlanArgs, NativeRetireApplyArgs, NativeRetireInspectArgs, NativeRetireResolveArgs:
		if s.NativeRetire == nil {
			return nil, result.New(result.DependencyMissing, nil)
		}
	default:
		return nil, result.New(result.DispatchInvalidRequest, nil)
	}
	switch args := input.(type) {
	case ModulePlanArgs:
		return s.ModulePublications.Plan(ctx, args)
	case ModuleApplyArgs:
		return s.ModulePublications.Apply(ctx, args)
	case ModuleInspectArgs:
		return s.ModulePublications.Inspect(ctx, args)
	case ModuleResolveArgs:
		return s.ModulePublications.Resolve(ctx, args)
	case NativeRetirePlanArgs:
		return s.NativeRetire.PlanNativeArchive(ctx, args)
	case NativeRetireApplyArgs:
		return s.NativeRetire.RetireNativeArchive(ctx, args)
	case NativeRetireInspectArgs:
		return s.NativeRetire.InspectNativeArchive(ctx, args)
	case NativeRetireResolveArgs:
		return s.NativeRetire.ResolveNativeArchive(ctx, args)
	default:
		return nil, result.New(result.DispatchInvalidRequest, nil)
	}
}
