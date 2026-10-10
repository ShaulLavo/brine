package dispatch

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type DataInitializationOperations interface {
	Plan(context.Context, datainit.Request) (datainit.Plan, error)
	Apply(context.Context, string, string) (datainit.Operation, error)
}
type DataInitPlanArgs datainit.Request
type DataInitApplyArgs struct {
	App    string `json:"app"`
	PlanID string `json:"plan_id"`
}

func decodeDataInitPlan(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "app", "first_release_plan", "artifact")
	if err != nil {
		return nil, err
	}
	var r DataInitPlanArgs
	if json.Unmarshal(raw, &r) != nil || !ValidApp(r.App) || !datainit.ValidID(r.FirstReleasePlan) || !datainit.ValidID(r.Artifact) || len(fields) != 3 {
		return nil, strictjson.ErrObject
	}
	return r, nil
}
func decodeDataInitApply(raw json.RawMessage) (any, error) {
	if _, err := strictjson.Object(raw, "app", "plan_id"); err != nil {
		return nil, err
	}
	var r DataInitApplyArgs
	if json.Unmarshal(raw, &r) != nil || !ValidApp(r.App) || !datainit.ValidID(r.PlanID) {
		return nil, strictjson.ErrObject
	}
	return r, nil
}
func DataInitializationFailure(err error) error {
	if errors.Is(err, datainit.ErrRecovery) {
		return result.New(result.RecoveryRequired, nil)
	}
	if err != nil {
		return result.New(result.PolicyRefused, nil)
	}
	return nil
}
