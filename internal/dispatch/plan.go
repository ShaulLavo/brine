package dispatch

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type PlanArgs struct {
	Spec string `json:"spec"`
}
type Planned struct {
	PlanID    string                  `json:"plan_id"`
	Kind      plan.Kind               `json:"kind"`
	Diff      *plan.ConfigurationDiff `json:"diff"`
	Conflicts []plan.Diagnostic       `json:"conflicts"`
}
type Planner interface {
	Plan(context.Context, spec.App) (Planned, error)
}

func decodePlan(raw json.RawMessage) (any, error) {
	f, err := strictjson.Object(raw, "spec")
	if err != nil {
		return nil, err
	}
	text, err := strictjson.Value[string](f["spec"])
	if err != nil {
		return nil, err
	}
	return spec.Parse([]byte(text))
}
func decodePlanned(raw json.RawMessage) (Planned, error) {
	_, err := strictjson.Object(raw, "plan_id", "kind", "diff", "conflicts")
	if err != nil {
		return Planned{}, err
	}
	p, err := strictjson.Value[Planned](raw)
	if err != nil || !jobs.ValidPlanID(p.PlanID) {
		return Planned{}, strictjson.ErrObject
	}
	switch p.Kind {
	case plan.Create, plan.Update, plan.NoOp, plan.Conflict:
	default:
		return Planned{}, strictjson.ErrObject
	}
	if p.Conflicts == nil || (p.Kind == plan.Conflict) != (len(p.Conflicts) > 0) {
		return Planned{}, strictjson.ErrObject
	}
	return p, nil
}
