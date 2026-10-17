// Package cdnoperator coordinates provider reads and one-shot effects against the
// enrolled host's durable CDN journal. It never saves an operation locally.
package cdnoperator

import (
	"context"
	"regexp"

	"github.com/ShaulLavo/brine/internal/cdn"
)

type PlanRequest struct {
	App              string          `json:"app"`
	Binding          string          `json:"binding"`
	Profile          cdn.ProfileRef  `json:"profile"`
	Mode             cdn.Mode        `json:"mode"`
	Revision         cdn.RevisionID  `json:"revision"`
	SamplePath       string          `json:"sample_path"`
	RecoverOperation cdn.OperationID `json:"recover_operation"`
}

type Planning struct {
	Context cdn.PlanningContext `json:"context"`
	Sample  cdn.AssetSample     `json:"sample"`
}

type ApplyRequest struct {
	PlanID         cdn.PlanID `json:"plan_id"`
	IdempotencyKey string     `json:"idempotency_key"`
}

type OperationRequest struct {
	Operation cdn.OperationID `json:"operation"`
}

type SealRequest struct {
	Operation cdn.OperationID  `json:"operation"`
	StepIndex uint32           `json:"step_index"`
	Sealed    cdn.SealedAction `json:"sealed"`
}

type ResultRequest struct {
	Operation cdn.OperationID  `json:"operation"`
	Result    cdn.EffectResult `json:"result"`
}

type InspectionRequest struct {
	Operation  cdn.OperationID `json:"operation"`
	Inspection cdn.Inspection  `json:"inspection"`
}

type ProofRequest struct {
	Operation cdn.OperationID   `json:"operation"`
	Proof     cdn.PublicProof   `json:"proof"`
	Readback  []cdn.Observation `json:"readback"`
}

type StatusRequest struct {
	App       string          `json:"app"`
	Operation cdn.OperationID `json:"operation"`
}

type Progress struct {
	Operation cdn.OperationID `json:"operation"`
	Plan      cdn.Plan        `json:"plan"`
	Work      cdn.Work        `json:"work"`
}

type Status struct {
	App         string            `json:"app"`
	Head        *cdn.Revision     `json:"head"`
	Pending     *cdn.Status       `json:"pending"`
	Plan        *cdn.Plan         `json:"plan"`
	Observation []cdn.Observation `json:"observation"`
	Proof       *cdn.PublicProof  `json:"proof"`
}

type Host interface {
	Context(context.Context, PlanRequest) (Planning, error)
	SavePlan(context.Context, cdn.Plan) (cdn.Plan, error)
	Apply(context.Context, ApplyRequest) (Progress, error)
	Next(context.Context, OperationRequest) (Progress, error)
	Seal(context.Context, SealRequest) (Progress, error)
	RecordResult(context.Context, ResultRequest) (Progress, error)
	RecordInspection(context.Context, InspectionRequest) (Progress, error)
	Qualify(context.Context, ProofRequest) (Progress, error)
	Status(context.Context, StatusRequest) (Status, error)
}

type Drivers interface {
	Edge(cdn.ProfileScope) (cdn.EdgeDriver, error)
	Traffic(cdn.ProfileScope) (cdn.TrafficDriver, error)
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
var appPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func (r PlanRequest) Valid() bool {
	return appPattern.MatchString(r.App) && namePattern.MatchString(r.Binding) && namePattern.MatchString(string(r.Profile)) && (r.Mode == cdn.Enabled || r.Mode == cdn.Direct) && (r.Revision == "" || digestPattern.MatchString(string(r.Revision))) && r.SamplePath != "" && len(r.SamplePath) <= 1024 && (r.RecoverOperation == "" || idPattern.MatchString(string(r.RecoverOperation)))
}

func (r ApplyRequest) Valid() bool {
	return digestPattern.MatchString(string(r.PlanID)) && idPattern.MatchString(r.IdempotencyKey)
}

func (r StatusRequest) Valid() bool {
	return appPattern.MatchString(r.App) && (r.Operation == "" || idPattern.MatchString(string(r.Operation)))
}

func ValidOperation(id cdn.OperationID) bool { return idPattern.MatchString(string(id)) }
