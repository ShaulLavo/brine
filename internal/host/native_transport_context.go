package host

import (
	"context"
	"time"

	"github.com/ShaulLavo/brine/internal/restic"
)

func nativeTransportContext(ctx context.Context, scope restic.Scope) (context.Context, context.CancelFunc, error) {
	deadline := scope.ExpiresAt.Add(-time.Second)
	maximum := time.Now().UTC().Add(9 * time.Minute)
	if deadline.After(maximum) {
		deadline = maximum
	}
	if prior, exists := ctx.Deadline(); exists && deadline.After(prior) {
		deadline = prior
	}
	if !deadline.After(time.Now().UTC()) {
		return nil, nil, errEngineSource
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	return bounded, cancel, nil
}
