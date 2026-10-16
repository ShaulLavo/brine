package store

import (
	"context"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
)

func readNativeRestoreTransitionInput(ctx context.Context, q dataQuerier, expected data.EngineTransition, stage liverestore.Stage) (data.EngineTransition, data.EngineTransitionCursor, error) {
	return readEngineTransition(ctx, q, expected.Common().Scope.ID)
}
