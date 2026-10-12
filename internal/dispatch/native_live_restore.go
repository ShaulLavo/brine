package dispatch

import (
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/result"
)

type NativeLiveRestoreOperations interface {
	Plan(context.Context, liverestore.PlanRequest) (liverestore.Plan, error)
	Apply(context.Context, liverestore.ApplyRequest) (jobs.Accepted, error)
	Inspect(context.Context, liverestore.InspectRequest) (liverestore.Progress, error)
	ReviewResolution(context.Context, liverestore.InspectRequest) (liverestore.Resolution, error)
	Resolve(context.Context, liverestore.ResolveRequest) (jobs.Accepted, error)
}

type NativeRestoreResolutionReviewArgs liverestore.InspectRequest

func decodeNativeRestoreResolutionReview(raw json.RawMessage) (any, error) {
	request, err := liverestore.DecodeInspectRequest(raw)
	return NativeRestoreResolutionReviewArgs(request), err
}
func decodeNativeRestoreResolve(raw json.RawMessage) (any, error) {
	return liverestore.DecodeResolveRequest(raw)
}

func decodeNativeLiveRestorePlan(raw json.RawMessage) (any, error) {
	return liverestore.DecodePlanRequest(raw)
}
func decodeNativeLiveRestoreApply(raw json.RawMessage) (any, error) {
	return liverestore.DecodeApplyRequest(raw)
}
func decodeNativeLiveRestoreInspect(raw json.RawMessage) (any, error) {
	return liverestore.DecodeInspectRequest(raw)
}

func (s *Server) nativeLiveRestore(ctx context.Context, args any) (any, error) {
	if s.NativeLiveRestore == nil {
		return nil, result.New(result.DependencyMissing, nil)
	}
	switch args := args.(type) {
	case NativeRestoreResolutionReviewArgs:
		return s.NativeLiveRestore.ReviewResolution(ctx, liverestore.InspectRequest(args))
	case liverestore.ResolveRequest:
		return s.NativeLiveRestore.Resolve(ctx, args)
	case liverestore.PlanRequest:
		return s.NativeLiveRestore.Plan(ctx, args)
	case liverestore.ApplyRequest:
		return s.NativeLiveRestore.Apply(ctx, args)
	case liverestore.InspectRequest:
		return s.NativeLiveRestore.Inspect(ctx, args)
	default:
		return nil, result.New(result.DispatchInvalidRequest, nil)
	}
}
