package dispatch

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type ApplyArgs struct {
	PlanID         string `json:"plan_id"`
	IdempotencyKey string `json:"idempotency_key"`
}
type OperationArgs struct {
	OperationID string `json:"operation_id"`
	AfterCursor uint64 `json:"after_cursor"`
}
type JobOperations interface {
	Apply(context.Context, string, string) (jobs.Accepted, error)
	Operation(context.Context, string, uint64) (jobs.Status, error)
}
type Authorization func(context.Context, Class) error

// WithJobs configures trusted host dependencies, never requester-provided code.
// Mutations remain closed when authorization is absent.
func (s *Server) WithJobs(jobs JobOperations, authorize Authorization) *Server {
	copy := *s
	copy.jobs, copy.authorize = jobs, authorize
	return &copy
}
func decodeApply(raw json.RawMessage) (any, error) {
	f, err := strictjson.Object(raw, "plan_id", "idempotency_key")
	if err != nil {
		return nil, err
	}
	id, err := strictjson.Value[string](f["plan_id"])
	if err != nil || !jobs.ValidPlanID(id) {
		return nil, strictjson.ErrObject
	}
	key, err := strictjson.Value[string](f["idempotency_key"])
	if err != nil || !jobs.ValidID(key) {
		return nil, strictjson.ErrObject
	}
	return ApplyArgs{PlanID: id, IdempotencyKey: key}, nil
}
func decodeOperation(raw json.RawMessage) (any, error) {
	f, err := strictjson.Object(raw, "operation_id", "after_cursor")
	if err != nil {
		return nil, err
	}
	id, err := strictjson.Value[string](f["operation_id"])
	if err != nil || !jobs.ValidID(id) {
		return nil, strictjson.ErrObject
	}
	cursor, err := strictjson.Value[uint64](f["after_cursor"])
	if err != nil {
		return nil, err
	}
	return OperationArgs{OperationID: id, AfterCursor: cursor}, nil
}
func decodeAccepted(raw json.RawMessage) (jobs.Accepted, error) {
	f, err := strictjson.Object(raw, "status", "operation_id")
	if err != nil {
		return jobs.Accepted{}, err
	}
	status, err := strictjson.Value[string](f["status"])
	if err != nil || status != "accepted" {
		return jobs.Accepted{}, strictjson.ErrObject
	}
	id, err := strictjson.Value[string](f["operation_id"])
	if err != nil || !jobs.ValidID(id) {
		return jobs.Accepted{}, strictjson.ErrObject
	}
	return jobs.Accepted{Status: status, OperationID: id}, nil
}
func decodeStatus(raw json.RawMessage) (jobs.Status, error) {
	bad := func() (jobs.Status, error) { return jobs.Status{}, strictjson.ErrObject }
	f, err := strictjson.Object(raw, "operation", "events", "next_cursor")
	if err != nil {
		return bad()
	}
	var identity struct {
		Kind ops.Kind `json:"kind"`
	}
	if json.Unmarshal(f["operation"], &identity) != nil {
		return bad()
	}
	keys := []string{"id", "plan_id", "kind", "app", "secret_ref", "state", "created_at", "updated_at"}
	if identity.Kind == ops.Resolve {
		keys = append(keys, "recovery_of")
	}
	opFields, err := strictjson.Object(f["operation"], keys...)
	if err != nil {
		return bad()
	}
	op, err := strictjson.Value[ops.Operation](f["operation"])
	if err != nil || !jobs.ValidID(op.ID) || !ops.ValidOperation(op) {
		return bad()
	}
	for _, key := range []string{"id", "plan_id", "kind", "app", "secret_ref", "state"} {
		if _, err := strictjson.Value[string](opFields[key]); err != nil {
			return bad()
		}
	}
	// Decode each timestamp as well as the object to reject null timestamps.
	for _, key := range []string{"created_at", "updated_at"} {
		if _, err := strictjson.Value[time.Time](opFields[key]); err != nil {
			return bad()
		}
	}
	raws, err := strictjson.Value[[]json.RawMessage](f["events"])
	if err != nil || len(raws) > jobs.EventPageLimit {
		return bad()
	}
	events := make([]ops.Event, 0, len(raws))
	var sequence uint64
	for _, raw := range raws {
		var header struct {
			Kind  string    `json:"kind"`
			State ops.State `json:"state"`
		}
		if json.Unmarshal(raw, &header) != nil {
			return bad()
		}
		fields := []string{"sequence", "kind", "created_at"}
		if header.State != "" {
			fields = append(fields, "state")
		}
		if header.Kind != "state" {
			fields = append(fields, "payload")
		}
		f, err := strictjson.Object(raw, fields...)
		if err != nil {
			return bad()
		}
		event, err := strictjson.Value[ops.Event](raw)
		if err != nil || event.Sequence == 0 || event.Sequence <= sequence || ops.ValidateOperationEvent(op, event) != nil {
			return bad()
		}
		if _, err := strictjson.Value[time.Time](f["created_at"]); err != nil {
			return bad()
		}
		sequence = event.Sequence
		events = append(events, event)
	}
	cursor, err := strictjson.Value[uint64](f["next_cursor"])
	if err != nil || (len(events) > 0 && cursor != sequence) {
		return bad()
	}
	return jobs.Status{Operation: op, Events: events, NextCursor: cursor}, nil
}
