package dispatch

import (
	"context"
	"encoding/json"

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
