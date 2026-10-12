package dispatch

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

// DeploymentState contains affirmative host evidence, never credential values.
type DeploymentState struct {
	ReviewHash string               `json:"review_hash"`
	Databases  []DeploymentDatabase `json:"databases"`
}
type DeploymentDatabase struct {
	Name        string `json:"name"`
	Prepared    bool   `json:"prepared"`
	Initialized bool   `json:"initialized"`
	Credentials bool   `json:"credentials"`
	Fenced      bool   `json:"fenced"`
}
type DeploymentReader interface {
	DeploymentState(context.Context, spec.App) (DeploymentState, error)
}
type deploymentArgs struct{ App spec.App }

func decodeDeployment(raw json.RawMessage) (any, error) {
	value, err := decodePlan(raw)
	if err != nil {
		return nil, err
	}
	return deploymentArgs{App: value.(spec.App)}, nil
}
func decodeDeploymentState(raw json.RawMessage) (DeploymentState, error) {
	fields, err := strictjson.Object(raw, "review_hash", "databases")
	if err != nil {
		return DeploymentState{}, err
	}
	p, err := strictjson.Value[DeploymentState](raw)
	if err != nil || !validDeploymentState(p) {
		return DeploymentState{}, strictjson.ErrObject
	}
	var databases []json.RawMessage
	if json.Unmarshal(fields["databases"], &databases) != nil {
		return DeploymentState{}, strictjson.ErrObject
	}
	for _, d := range databases {
		if _, err := strictjson.Object(d, "name", "prepared", "initialized", "credentials", "fenced"); err != nil {
			return DeploymentState{}, err
		}
	}
	return p, nil
}
func validDeploymentState(p DeploymentState) bool {
	if !jobs.ValidPlanID(p.ReviewHash) || p.Databases == nil || len(p.Databases) > 16 {
		return false
	}
	names := map[string]bool{}
	for _, d := range p.Databases {
		if !ValidApp(d.Name) || names[d.Name] || !d.Prepared && (d.Initialized || d.Credentials || d.Fenced) {
			return false
		}
		names[d.Name] = true
	}
	return true
}

// DeploymentOperationQuery recovers an accepted job without submitting work or secrets.
type DeploymentOperationQuery struct {
	PlanID string   `json:"plan_id"`
	Kind   ops.Kind `json:"kind"`
	Key    string   `json:"idempotency_key"`
}
type DeploymentOperation struct {
	OperationID string `json:"operation_id"`
}
type DeploymentOperationReader interface {
	DeploymentOperation(context.Context, DeploymentOperationQuery) (DeploymentOperation, error)
}

func decodeDeploymentOperationQuery(raw json.RawMessage) (any, error) {
	if _, err := strictjson.Object(raw, "plan_id", "kind", "idempotency_key"); err != nil {
		return nil, err
	}
	q, err := strictjson.Value[DeploymentOperationQuery](raw)
	if err != nil || !jobs.ValidPlanID(q.PlanID) || (q.Kind != ops.Deploy && q.Kind != ops.CredentialActivation && q.Kind != ops.DataInitApply) || (q.Kind == ops.Deploy && !jobs.ValidID(q.Key)) || (q.Kind != ops.Deploy && q.Key != "") {
		return nil, strictjson.ErrObject
	}
	return q, nil
}
func decodeDeploymentOperation(raw json.RawMessage) (DeploymentOperation, error) {
	if _, err := strictjson.Object(raw, "operation_id"); err != nil {
		return DeploymentOperation{}, err
	}
	value, err := strictjson.Value[DeploymentOperation](raw)
	if err != nil || value.OperationID != "" && !jobs.ValidID(value.OperationID) {
		return DeploymentOperation{}, strictjson.ErrObject
	}
	return value, nil
}

// DeploymentCheckpointArgs asks the host to validate an advisory reference
// against the actual app, target and desired input, not a client-supplied label.
type DeploymentCheckpointArgs struct {
	Spec            string                `json:"spec"`
	SchemaArtifacts []data.SchemaArtifact `json:"schema_artifacts"`
	ManagedModule   *ManagedModuleInput   `json:"managed_module,omitempty"`
	PlanID          string                `json:"plan_id"`
	Step            string                `json:"step"`
	Database        string                `json:"database"`
}
type deploymentCheckpointArgs struct {
	Request DeploymentCheckpointArgs
	App     spec.App
}
type DeploymentCheckpoint struct {
	PlanID     string   `json:"plan_id"`
	ReviewHash string   `json:"review_hash"`
	Planned    *Planned `json:"planned"`
}
type DeploymentCheckpointReader interface {
	DeploymentCheckpoint(context.Context, spec.App, DeploymentCheckpointArgs) (DeploymentCheckpoint, error)
}

func decodeDeploymentCheckpointArgs(raw json.RawMessage) (any, error) {
	f, err := planFields(raw, "plan_id", "step", "database")
	if err != nil {
		return nil, err
	}
	q, err := strictjson.Value[DeploymentCheckpointArgs](raw)
	if err != nil || !jobs.ValidPlanID(q.PlanID) || (q.Step != "preparation" && q.Step != "release" && q.Step != "credentials" && q.Step != "initialization") || ((q.Step == "credentials" || q.Step == "initialization") && !ValidApp(q.Database)) || ((q.Step == "preparation" || q.Step == "release") && q.Database != "") {
		return nil, strictjson.ErrObject
	}
	planFields := map[string]json.RawMessage{"spec": f["spec"], "schema_artifacts": f["schema_artifacts"]}
	if value := f["managed_module"]; value != nil {
		planFields["managed_module"] = value
	}
	planRaw, err := json.Marshal(planFields)
	if err != nil {
		return nil, err
	}
	app, err := decodePlan(planRaw)
	if err != nil {
		return nil, err
	}
	return deploymentCheckpointArgs{Request: q, App: app.(spec.App)}, nil
}
func decodeDeploymentCheckpoint(raw json.RawMessage) (DeploymentCheckpoint, error) {
	f, err := strictjson.Object(raw, "plan_id", "review_hash", "planned")
	if err != nil {
		return DeploymentCheckpoint{}, err
	}
	q, err := strictjson.Value[DeploymentCheckpoint](raw)
	if err != nil || !jobs.ValidPlanID(q.PlanID) || !jobs.ValidPlanID(q.ReviewHash) {
		return DeploymentCheckpoint{}, strictjson.ErrObject
	}
	if q.Planned != nil {
		p, err := decodePlanned(f["planned"])
		if err != nil || p.PlanID != q.PlanID {
			return DeploymentCheckpoint{}, strictjson.ErrObject
		}
		q.Planned = &p
	}
	return q, nil
}
