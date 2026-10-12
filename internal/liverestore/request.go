package liverestore

import (
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

const RequestLimit = 4096

type PlanRequest struct {
	App                    string `json:"app"`
	Database               string `json:"database"`
	SelectedPointID        string `json:"selected_point_id"`
	CurrentPreparedPointID string `json:"current_prepared_point_id"`
	RecoveryReleaseID      string `json:"recovery_release_id"`
}

type ApplyRequest struct {
	App            string `json:"app"`
	PlanID         string `json:"plan_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type InspectRequest struct {
	OperationID string `json:"operation_id"`
}

type ResolveRequest struct {
	OriginalOperationID      string `json:"original_operation_id"`
	ReviewedResolutionPlanID string `json:"reviewed_resolution_plan_id"`
	IdempotencyKey           string `json:"idempotency_key"`
}

func DecodePlanRequest(raw []byte) (PlanRequest, error) {
	value, err := decodeRequest[PlanRequest](raw, "app", "database", "selected_point_id", "current_prepared_point_id", "recovery_release_id")
	if err != nil || !appName.MatchString(value.App) || !appName.MatchString(value.Database) || !data.ValidID(value.SelectedPointID) ||
		!data.ValidID(value.CurrentPreparedPointID) || value.SelectedPointID == value.CurrentPreparedPointID || !operationID.MatchString(value.RecoveryReleaseID) {
		return PlanRequest{}, ErrRecord
	}
	return value, nil
}

func DecodeApplyRequest(raw []byte) (ApplyRequest, error) {
	value, err := decodeRequest[ApplyRequest](raw, "app", "plan_id", "idempotency_key")
	if err != nil || !appName.MatchString(value.App) || !planID(value.PlanID) || !operationID.MatchString(value.IdempotencyKey) {
		return ApplyRequest{}, ErrRecord
	}
	return value, nil
}

func DecodeInspectRequest(raw []byte) (InspectRequest, error) {
	value, err := decodeRequest[InspectRequest](raw, "operation_id")
	if err != nil || !operationID.MatchString(value.OperationID) {
		return InspectRequest{}, ErrRecord
	}
	return value, nil
}

func DecodeResolveRequest(raw []byte) (ResolveRequest, error) {
	value, err := decodeRequest[ResolveRequest](raw, "original_operation_id", "reviewed_resolution_plan_id", "idempotency_key")
	if err != nil || !operationID.MatchString(value.OriginalOperationID) || !planID(value.ReviewedResolutionPlanID) || !operationID.MatchString(value.IdempotencyKey) {
		return ResolveRequest{}, ErrRecord
	}
	return value, nil
}

func decodeRequest[T any](raw []byte, fields ...string) (T, error) {
	var value T
	if len(raw) == 0 || len(raw) > RequestLimit {
		return value, ErrRecord
	}
	if _, err := strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, &value) != nil {
		return value, ErrRecord
	}
	return value, nil
}
