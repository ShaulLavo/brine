package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/replicapermits"
)

// WriterAttemptRuntime has no deployment managers or ambient agent authority.
type WriterAttemptRuntime struct {
	state   *gatedStore
	attempt replicapermits.WriterAttempts
}

func (r *WriterAttemptRuntime) WriterAttempt(ctx context.Context, id string) error {
	return r.attempt.WriterAttempt(ctx, id)
}
func (r *WriterAttemptRuntime) Close() error { return r.state.Close() }
