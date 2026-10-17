package dispatch

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/cdnoperator"
	"github.com/ShaulLavo/brine/internal/result"
)

type CDNSavePlanArgs struct {
	Plan cdn.Plan `json:"plan"`
}

func decodeCDNContext(raw json.RawMessage) (any, error) {
	var request cdnoperator.PlanRequest
	if cdnoperator.DecodeExact(raw, &request) != nil || !request.Valid() {
		return nil, cdn.ErrInvalid
	}
	return request, nil
}

func decodeCDNPlan(raw json.RawMessage) (any, error) {
	var request CDNSavePlanArgs
	if cdnoperator.DecodeExact(raw, &request) != nil || request.Plan.Validate() != nil {
		return nil, cdn.ErrInvalid
	}
	return request, nil
}

func decodeCDNApply(raw json.RawMessage) (any, error) {
	var request cdnoperator.ApplyRequest
	if cdnoperator.DecodeExact(raw, &request) != nil || !request.Valid() {
		return nil, cdn.ErrInvalid
	}
	return request, nil
}

func decodeCDNNext(raw json.RawMessage) (any, error) {
	var request cdnoperator.OperationRequest
	if cdnoperator.DecodeExact(raw, &request) != nil || !cdnoperator.ValidOperation(request.Operation) {
		return nil, cdn.ErrInvalid
	}
	return request, nil
}

func decodeCDNSeal(raw json.RawMessage) (any, error) {
	var request cdnoperator.SealRequest
	if cdnoperator.DecodeExact(raw, &request) != nil || !cdnoperator.ValidOperation(request.Operation) || request.StepIndex >= cdn.MaxSteps || request.Sealed.Action.Attachment.Validate() != nil {
		return nil, cdn.ErrInvalid
	}
	return request, nil
}

func decodeCDNResult(raw json.RawMessage) (any, error) {
	var request cdnoperator.ResultRequest
	if json.Unmarshal(raw, &request) != nil {
		return nil, cdn.ErrInvalid
	}
	return request, nil
}

func decodeCDNInspection(raw json.RawMessage) (any, error) {
	var request cdnoperator.InspectionRequest
	if json.Unmarshal(raw, &request) != nil {
		return nil, cdn.ErrInvalid
	}
	return request, nil
}

func decodeCDNProof(raw json.RawMessage) (any, error) {
	var request cdnoperator.ProofRequest
	if cdnoperator.DecodeExact(raw, &request) != nil || !cdnoperator.ValidOperation(request.Operation) {
		return nil, cdn.ErrInvalid
	}
	return request, nil
}

func decodeCDNStatus(raw json.RawMessage) (any, error) {
	var request cdnoperator.StatusRequest
	if cdnoperator.DecodeExact(raw, &request) != nil || !request.Valid() {
		return nil, cdn.ErrInvalid
	}
	return request, nil
}

func (s *Server) cdnOperation(ctx context.Context, args any) (any, error) {
	if s.CDN == nil {
		return nil, result.New(result.DependencyMissing, nil)
	}
	switch request := args.(type) {
	case cdnoperator.PlanRequest:
		return s.CDN.Context(ctx, request)
	case CDNSavePlanArgs:
		return s.CDN.SavePlan(ctx, request.Plan)
	case cdnoperator.ApplyRequest:
		return s.CDN.Apply(ctx, request)
	case cdnoperator.OperationRequest:
		return s.CDN.Next(ctx, request)
	case cdnoperator.SealRequest:
		return s.CDN.Seal(ctx, request)
	case cdnoperator.ResultRequest:
		return s.CDN.RecordResult(ctx, request)
	case cdnoperator.InspectionRequest:
		return s.CDN.RecordInspection(ctx, request)
	case cdnoperator.ProofRequest:
		return s.CDN.Qualify(ctx, request)
	case cdnoperator.StatusRequest:
		return s.CDN.Status(ctx, request)
	default:
		return nil, result.New(result.DispatchOperationRefused, nil)
	}
}

func CDNError(err error) error {
	if errors.Is(err, cdn.ErrInvalid) {
		return result.New(result.InvalidUsage, err)
	}
	if errors.Is(err, cdn.ErrConflict) {
		return result.New(result.Conflict, err)
	}
	return result.Classify(err)
}

func decodeCDNPlanning(raw json.RawMessage) (cdnoperator.Planning, error) {
	var planning cdnoperator.Planning
	if cdnoperator.DecodeExact(raw, &planning) != nil || planning.Context.Attachment.Validate() != nil || (cdn.Qualification{Attachment: planning.Context.Attachment, Sample: planning.Sample}).Validate() != nil {
		return cdnoperator.Planning{}, cdn.ErrInvalid
	}
	return planning, nil
}

func decodeCDNStatusResponse(raw json.RawMessage) (cdnoperator.Status, error) {
	var status cdnoperator.Status
	if cdnoperator.DecodeExact(raw, &status) != nil || !ValidApp(status.App) {
		return cdnoperator.Status{}, cdn.ErrInvalid
	}
	if status.Plan != nil && status.Plan.Validate() != nil || status.Pending != nil && status.Pending.Plan.Validate() != nil {
		return cdnoperator.Status{}, cdn.ErrInvalid
	}
	return status, nil
}
