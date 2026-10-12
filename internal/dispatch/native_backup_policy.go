package dispatch

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type NativeBackupPolicyPlanArgs struct {
	DeploymentPlanID string `json:"deployment_plan_id"`
}
type NativeBackupPolicyActivateArgs struct {
	PlanID         string `json:"plan_id"`
	IdempotencyKey string `json:"idempotency_key"`
}
type NativeBackupPolicyPlanView struct {
	PlanID                      string    `json:"plan_id"`
	BindingID                   string    `json:"binding_id"`
	EpochID                     string    `json:"epoch_id"`
	PreviousRevision            uint64    `json:"previous_revision"`
	CaptureIntervalSeconds      int       `json:"capture_interval_seconds"`
	VerificationIntervalSeconds int       `json:"verification_interval_seconds"`
	ExpiresAt                   time.Time `json:"expires_at"`
}

func (v NativeBackupPolicyPlanView) Valid() bool {
	return data.ValidID(v.PlanID) && data.ValidID(v.BindingID) && data.ValidID(v.EpochID) && v.CaptureIntervalSeconds > 0 && v.VerificationIntervalSeconds > 0 && !v.ExpiresAt.IsZero() && v.ExpiresAt.Location() == time.UTC
}

type NativeBackupPolicyOperations interface {
	PlanPolicy(context.Context, NativeBackupPolicyPlanArgs) (NativeBackupPolicyPlanView, error)
	ActivatePolicy(context.Context, NativeBackupPolicyActivateArgs) (jobs.Accepted, error)
}

func decodeNativeBackupPolicyPlan(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "deployment_plan_id")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	id, err := strictjson.Value[string](fields["deployment_plan_id"])
	if err != nil || !jobs.ValidPlanID(id) {
		return nil, strictjson.ErrObject
	}
	return NativeBackupPolicyPlanArgs{DeploymentPlanID: id}, nil
}
func decodeNativeBackupPolicyActivate(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "plan_id", "idempotency_key")
	if err != nil {
		return nil, strictjson.ErrObject
	}
	plan, planErr := strictjson.Value[string](fields["plan_id"])
	key, keyErr := strictjson.Value[string](fields["idempotency_key"])
	if planErr != nil || keyErr != nil || !data.ValidID(plan) || !data.ValidID(key) {
		return nil, strictjson.ErrObject
	}
	return NativeBackupPolicyActivateArgs{PlanID: plan, IdempotencyKey: key}, nil
}
func DecodeNativeBackupPolicyPlanView(raw []byte) (NativeBackupPolicyPlanView, error) {
	var view NativeBackupPolicyPlanView
	if _, err := strictjson.Object(raw, "plan_id", "binding_id", "epoch_id", "previous_revision", "capture_interval_seconds", "verification_interval_seconds", "expires_at"); err != nil || json.Unmarshal(raw, &view) != nil || !view.Valid() {
		return view, strictjson.ErrObject
	}
	return view, nil
}
