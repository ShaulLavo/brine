package dispatch

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type ResolveArgs struct {
	OperationID    string `json:"operation_id"`
	IdempotencyKey string `json:"idempotency_key"`
}
type ResolutionJobs interface {
	Resolve(context.Context, string, string) (jobs.Accepted, error)
}

func decodeResolve(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "operation_id", "idempotency_key")
	if err != nil {
		return nil, err
	}
	id, err := strictjson.Value[string](fields["operation_id"])
	if err != nil || !jobs.ValidID(id) {
		return nil, strictjson.ErrObject
	}
	key, err := strictjson.Value[string](fields["idempotency_key"])
	if err != nil || !jobs.ValidID(key) {
		return nil, strictjson.ErrObject
	}
	return ResolveArgs{OperationID: id, IdempotencyKey: key}, nil
}
