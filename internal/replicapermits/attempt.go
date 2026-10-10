package replicapermits

import (
	"context"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/store"
)

type OperationEvidence interface {
	OperationActive(context.Context, string) (bool, error)
}
type AttemptStore interface {
	ReadWriterStart(context.Context, data.AppIncarnationID) (store.WriterStartResolution, error)
	RecordWriterAttempt(context.Context, data.AppIncarnationID, string) error
}

// WriterAttempts is the sole startup mutation. The preceding read-only permit
// checks schema/absence; this consumes untouched allocation evidence before ExecStart.
type WriterAttempts struct {
	State      AttemptStore
	Operations OperationEvidence
}

func (a WriterAttempts) WriterAttempt(ctx context.Context, incarnation string) error {
	if ctx.Err() != nil || a.State == nil || !data.ValidID(incarnation) {
		return replication.ErrPermit
	}
	id := data.AppIncarnationID(incarnation)
	start, err := a.State.ReadWriterStart(ctx, id)
	if err != nil || ctx.Err() != nil {
		return replication.ErrPermit
	}
	switch start.State {
	case store.WriterStartNone:
		if start.Intent != nil {
			return replication.ErrPermit
		}
		return nil
	case store.WriterStartPending:
		if a.Operations == nil || start.Intent == nil || start.Intent.IncarnationID != id || start.Intent.OperationID == "" {
			return replication.ErrPermit
		}
		active, err := a.Operations.OperationActive(ctx, start.Intent.OperationID)
		if err != nil || !active || ctx.Err() != nil {
			return replication.ErrPermit
		}
		if err = a.State.RecordWriterAttempt(ctx, id, start.Intent.OperationID); err != nil || ctx.Err() != nil {
			return replication.ErrPermit
		}
		return nil
	default:
		return replication.ErrPermit
	}
}
