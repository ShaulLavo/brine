package dispatch

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type PlanArgs struct {
	Spec            string                `json:"spec"`
	SchemaArtifacts []data.SchemaArtifact `json:"schema_artifacts"`
}
type Planned struct {
	PlanID          string                  `json:"plan_id"`
	Kind            plan.Kind               `json:"kind"`
	Diff            *plan.ConfigurationDiff `json:"diff"`
	SchemaArtifacts []data.SchemaArtifact   `json:"schema_artifacts"`
	Conflicts       []plan.Diagnostic       `json:"conflicts"`
}
type Planner interface {
	Plan(context.Context, spec.App) (Planned, error)
}

func decodePlan(raw json.RawMessage) (any, error) {
	f, err := strictjson.Object(raw, "spec", "schema_artifacts")
	if err != nil {
		return nil, err
	}
	text, err := strictjson.Value[string](f["spec"])
	if err != nil {
		return nil, err
	}
	app, err := spec.Parse([]byte(text))
	if err != nil {
		return nil, err
	}
	var artifacts []data.SchemaArtifact
	if rawArtifacts, ok := f["schema_artifacts"]; ok {
		if err := json.Unmarshal(rawArtifacts, &artifacts); err != nil {
			return nil, err
		}
	}
	if err := app.ResolveSchemas(artifacts); err != nil {
		return nil, err
	}
	return app, nil
}
func decodePlanned(raw json.RawMessage) (Planned, error) {
	_, err := strictjson.Object(raw, "plan_id", "kind", "diff", "conflicts", "schema_artifacts")
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

type PreparationPlanner interface {
	PlanDataPreparation(context.Context, spec.App) (Planned, error)
}
type preparationArgs struct{ App spec.App }

func decodePreparation(raw json.RawMessage) (any, error) {
	value, err := decodePlan(raw)
	if err != nil {
		return nil, err
	}
	return preparationArgs{App: value.(spec.App)}, nil
}
