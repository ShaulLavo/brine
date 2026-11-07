package dispatch

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type SQLiteRetireInspectArgs struct {
	ArchiveID data.ArchiveID `json:"archive_id"`
}
type SQLiteRetireResolveArgs struct {
	OperationID    string `json:"operation_id"`
	Disposition    string `json:"disposition"`
	IdempotencyKey string `json:"idempotency_key"`
}
type SQLiteRetireOperations interface {
	InspectSQLiteArchive(context.Context, SQLiteRetireInspectArgs) (archive.Inspection, error)
	ResolveSQLiteArchive(context.Context, SQLiteRetireResolveArgs) (jobs.Accepted, error)
}

func decodeSQLiteRetireInspect(raw json.RawMessage) (any, error) {
	var a SQLiteRetireInspectArgs
	if _, err := strictjson.Object(raw, "archive_id"); err != nil || json.Unmarshal(raw, &a) != nil || !data.ValidID(string(a.ArchiveID)) {
		return nil, strictjson.ErrObject
	}
	return a, nil
}
func decodeSQLiteRetireResolve(raw json.RawMessage) (any, error) {
	var a SQLiteRetireResolveArgs
	if _, err := strictjson.Object(raw, "operation_id", "disposition", "idempotency_key"); err != nil || json.Unmarshal(raw, &a) != nil || !jobs.ValidID(a.OperationID) || !jobs.ValidID(a.IdempotencyKey) || a.Disposition != "continue" && a.Disposition != "abandon" {
		return nil, strictjson.ErrObject
	}
	return a, nil
}
func (s *Server) sqliteRetire(ctx context.Context, input any) (any, error) {
	if s.SQLiteRetire == nil {
		return nil, result.New(result.DependencyMissing, nil)
	}
	switch a := input.(type) {
	case SQLiteRetireInspectArgs:
		return s.SQLiteRetire.InspectSQLiteArchive(ctx, a)
	case SQLiteRetireResolveArgs:
		return s.SQLiteRetire.ResolveSQLiteArchive(ctx, a)
	default:
		return nil, result.New(result.DispatchInvalidRequest, nil)
	}
}
