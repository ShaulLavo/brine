package dispatch

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

// SQLitePrepareArgs selects a database and, for a supersede, the unconsumed
// predecessor. The host derives the preparation ID and every scope fact.
type SQLitePrepareArgs struct {
	App            string `json:"app"`
	Database       string `json:"database"`
	Supersedes     string `json:"supersedes"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (a SQLitePrepareArgs) Valid() bool {
	return ValidApp(a.App) && (a.Database == "" || ValidApp(a.Database)) && (a.Supersedes == "" || data.ValidID(a.Supersedes)) && data.ValidID(a.IdempotencyKey)
}

type SQLiteAbandonArgs struct {
	PreparationID  string `json:"preparation_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (a SQLiteAbandonArgs) Valid() bool {
	return data.ValidID(a.PreparationID) && data.ValidID(a.IdempotencyKey)
}

type SQLitePreparationOperations interface {
	Prepare(context.Context, SQLitePrepareArgs) (jobs.Accepted, error)
	Abandon(context.Context, SQLiteAbandonArgs) (jobs.Accepted, error)
}

// stringFields strictly decodes an object of exactly these string fields.
func stringFields(raw json.RawMessage, names ...string) ([]string, error) {
	f, err := strictjson.Object(raw, names...)
	if err != nil {
		return nil, strictjson.ErrObject
	}
	values := make([]string, len(names))
	for i, name := range names {
		if values[i], err = strictjson.Value[string](f[name]); err != nil {
			return nil, strictjson.ErrObject
		}
	}
	return values, nil
}

func decodeSQLitePrepare(raw json.RawMessage) (any, error) {
	v, err := stringFields(raw, "app", "database", "supersedes", "idempotency_key")
	if err != nil {
		return nil, err
	}
	a := SQLitePrepareArgs{App: v[0], Database: v[1], Supersedes: v[2], IdempotencyKey: v[3]}
	if !a.Valid() {
		return nil, strictjson.ErrObject
	}
	return a, nil
}

func decodeSQLiteAbandon(raw json.RawMessage) (any, error) {
	v, err := stringFields(raw, "preparation_id", "idempotency_key")
	if err != nil {
		return nil, err
	}
	a := SQLiteAbandonArgs{PreparationID: v[0], IdempotencyKey: v[1]}
	if !a.Valid() {
		return nil, strictjson.ErrObject
	}
	return a, nil
}

func (s *Server) submitSQLitePreparation(ctx context.Context, args any) (jobs.Accepted, error) {
	if s.SQLitePreparation == nil {
		return jobs.Accepted{}, result.New(result.DependencyMissing, nil)
	}
	switch args := args.(type) {
	case SQLitePrepareArgs:
		return s.SQLitePreparation.Prepare(ctx, args)
	case SQLiteAbandonArgs:
		return s.SQLitePreparation.Abandon(ctx, args)
	default:
		return jobs.Accepted{}, result.New(result.DispatchInvalidRequest, nil)
	}
}
