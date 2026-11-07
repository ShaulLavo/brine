//go:build linux

package host

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

func (o sqliteRetireOperations) replay(ctx context.Context, in store.SQLiteRetireInput, key string) (jobs.Accepted, bool, error) {
	if !jobs.ValidID(key) {
		return jobs.Accepted{}, false, result.New(result.InvalidUsage, nil)
	}
	op, found, err := o.state.FindSQLiteRetireOperation(ctx, in, o.jobs.Requester, key)
	if errors.Is(err, store.ErrConflict) {
		return jobs.Accepted{}, false, result.New(result.Conflict, err)
	}
	if err != nil || !found {
		return jobs.Accepted{}, false, err
	}
	return jobs.Accepted{Status: "accepted", OperationID: op.ID}, true, nil
}
