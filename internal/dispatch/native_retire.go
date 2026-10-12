package dispatch

import (
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type NativeRetirePlanArgs struct {
	App string `json:"app"`
}
type NativeRetireApplyArgs struct {
	App            string `json:"app"`
	PlanID         string `json:"plan_id"`
	IdempotencyKey string `json:"idempotency_key"`
}
type NativeRetireInspectArgs struct {
	ArchiveID data.ArchiveID `json:"archive_id"`
}
type NativeRetireResolveArgs struct {
	OperationID    string `json:"operation_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type NativeRetireOperations interface {
	PlanNativeArchive(context.Context, NativeRetirePlanArgs) (archive.Plan, error)
	RetireNativeArchive(context.Context, NativeRetireApplyArgs) (jobs.Accepted, error)
	InspectNativeArchive(context.Context, NativeRetireInspectArgs) (archive.Inspection, error)
	ResolveNativeArchive(context.Context, NativeRetireResolveArgs) (jobs.Accepted, error)
}

func decodeNativeRetireResolve(raw json.RawMessage) (any, error) {
	var a NativeRetireResolveArgs
	if _, err := strictjson.Object(raw, "operation_id", "idempotency_key"); err != nil || json.Unmarshal(raw, &a) != nil || !jobs.ValidID(a.OperationID) || !data.ValidID(a.IdempotencyKey) {
		return nil, strictjson.ErrObject
	}
	return a, nil
}

func decodeNativeRetirePlan(raw json.RawMessage) (any, error) {
	var a NativeRetirePlanArgs
	if _, err := strictjson.Object(raw, "app"); err != nil || json.Unmarshal(raw, &a) != nil || !ValidApp(a.App) {
		return nil, strictjson.ErrObject
	}
	return a, nil
}
func decodeNativeRetireApply(raw json.RawMessage) (any, error) {
	var a NativeRetireApplyArgs
	if _, err := strictjson.Object(raw, "app", "plan_id", "idempotency_key"); err != nil || json.Unmarshal(raw, &a) != nil || !ValidApp(a.App) || !jobs.ValidPlanID(a.PlanID) || !data.ValidID(a.IdempotencyKey) {
		return nil, strictjson.ErrObject
	}
	return a, nil
}
func decodeNativeRetireInspect(raw json.RawMessage) (any, error) {
	var a NativeRetireInspectArgs
	if _, err := strictjson.Object(raw, "archive_id"); err != nil || json.Unmarshal(raw, &a) != nil || !data.ValidID(string(a.ArchiveID)) {
		return nil, strictjson.ErrObject
	}
	return a, nil
}
func (s *Server) nativeRetire(ctx context.Context, args any) (any, error) {
	if s.NativeRetire == nil {
		return nil, result.New(result.DependencyMissing, nil)
	}
	switch a := args.(type) {
	case NativeRetirePlanArgs:
		return s.NativeRetire.PlanNativeArchive(ctx, a)
	case NativeRetireApplyArgs:
		return s.NativeRetire.RetireNativeArchive(ctx, a)
	case NativeRetireInspectArgs:
		return s.NativeRetire.InspectNativeArchive(ctx, a)
	case NativeRetireResolveArgs:
		return s.NativeRetire.ResolveNativeArchive(ctx, a)
	default:
		return nil, result.New(result.DispatchInvalidRequest, nil)
	}
}
