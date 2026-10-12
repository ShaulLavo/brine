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
	ManagedModule   *ManagedModuleInput   `json:"managed_module,omitempty"`
}
type Planned struct {
	Managed         *ManagedPlanSummary     `json:"managed,omitempty"`
	PlanID          string                  `json:"plan_id"`
	Kind            plan.Kind               `json:"kind"`
	Diff            *plan.ConfigurationDiff `json:"diff"`
	SchemaArtifacts []data.SchemaArtifact   `json:"schema_artifacts"`
	Conflicts       []plan.Diagnostic       `json:"conflicts"`
}

type ManagedPlanSummary struct {
	State    data.EngineDataState `json:"state"`
	Frontend bool                 `json:"frontend"`
}
type Planner interface {
	Plan(context.Context, spec.App) (Planned, error)
}

func decodePlan(raw json.RawMessage) (any, error) {
	f, err := planFields(raw)
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
	if app.SpacetimeDB != nil {
		if _, err := strictjson.Value[[]json.RawMessage](f["schema_artifacts"]); err != nil {
			return nil, err
		}
	}
	artifacts, err := decodeSchemaArtifacts(f["schema_artifacts"])
	if err != nil {
		return nil, err
	}
	if err := app.ResolveSchemas(artifacts); err != nil {
		return nil, err
	}
	if err := attachManagedModule(&app, f["managed_module"]); err != nil {
		return nil, err
	}
	return app, nil
}
func decodePlanned(raw json.RawMessage) (Planned, error) {
	keys := []string{"plan_id", "kind", "diff", "conflicts", "schema_artifacts"}
	var presence map[string]json.RawMessage
	if json.Unmarshal(raw, &presence) != nil {
		return Planned{}, strictjson.ErrObject
	}
	if _, present := presence["managed"]; present {
		keys = append(keys, "managed")
	}
	fields, err := strictjson.Object(raw, keys...)
	if err != nil {
		return Planned{}, err
	}
	p, err := strictjson.Value[Planned](raw)
	if err != nil || !jobs.ValidPlanID(p.PlanID) {
		return Planned{}, strictjson.ErrObject
	}
	if _, err := decodeSchemaArtifacts(fields["schema_artifacts"]); err != nil {
		return Planned{}, err
	}
	if managed := fields["managed"]; managed != nil {
		if _, err := strictjson.Object(managed, "state", "frontend"); err != nil || p.Managed == nil || p.Managed.State != data.EngineUnpublished && p.Managed.State != data.EngineInitialized || len(p.SchemaArtifacts) != 0 {
			return Planned{}, strictjson.ErrObject
		}
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

func decodeSchemaArtifacts(raw json.RawMessage) ([]data.SchemaArtifact, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || len(items) > 16 {
		return nil, strictjson.ErrObject
	}
	artifacts := make([]data.SchemaArtifact, 0, len(items))
	for _, item := range items {
		fields, err := strictjson.Object(item, "definition", "digest")
		if err != nil {
			return nil, err
		}
		if _, err := strictjson.Object(fields["definition"], "database", "marker", "catalog_sha256"); err != nil {
			return nil, err
		}
		artifact, err := strictjson.Value[data.SchemaArtifact](item)
		if err != nil || !ValidApp(string(artifact.Definition.Database)) || artifact.Definition.Marker == data.EmptyMarker || !jobs.ValidPlanID(artifact.Digest) || !data.ValidMarker(artifact.Definition.Marker) || !data.ValidCatalogHash(artifact.Definition.CatalogSHA256) {
			return nil, strictjson.ErrObject
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}
