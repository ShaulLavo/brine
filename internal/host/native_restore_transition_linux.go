//go:build linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/store"
)

func relocateNativeRestoreTree(ctx context.Context, state *store.Store, capability *store.NativeRestoreTransition, tree *engineTransitionTree) error {
	return relocateEngineTransitionTree(ctx, engineTransitionControl{state: state, restoration: capability}, tree)
}
