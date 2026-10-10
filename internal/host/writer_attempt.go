package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/replicapermits"
	"github.com/ShaulLavo/brine/internal/store"
)

// WriterAttemptRuntime has no deployment managers or ambient agent authority.
type WriterAttemptRuntime struct {
	state   *store.Store
	attempt replicapermits.WriterAttempts
}

func (r *WriterAttemptRuntime) WriterAttempt(ctx context.Context, id string) error {
	return r.attempt.WriterAttempt(ctx, id)
}
func (r *WriterAttemptRuntime) Close() error { return r.state.Close() }
