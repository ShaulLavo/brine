//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
)

// relocateNativeRestoreTree executes only newly journaled moves. A retained
// pending move is adopted through observed layout and directory fsync; a pending
// move whose effect cannot be positively observed stays fenced for resolution.
func relocateNativeRestoreTree(ctx context.Context, state *store.Store, capability *store.NativeRestoreTransition, tree *engineTransitionTree) error {
	if state == nil || tree == nil || !tree.active() {
		return errEngineSource
	}
	for {
		view, err := state.ReadNativeRestoreTransition(ctx, capability)
		if err != nil || view.Input.SHA256() != tree.input.SHA256() {
			return errEngineSource
		}
		if view.Cursor.Stage == data.EngineTransitionActivationIntent || view.Cursor.Stage == data.EngineTransitionActivated || view.Cursor.Stage == data.EngineTransitionFinishIntent {
			return nil
		}
		if err = relocateNativeRestoreStep(ctx, state, capability, tree, view.Cursor); err != nil {
			return err
		}
	}
}

func relocateNativeRestoreStep(ctx context.Context, state *store.Store, capability *store.NativeRestoreTransition, tree *engineTransitionTree, cursor data.EngineTransitionCursor) error {
	switch cursor.Stage {
	case data.EngineTransitionPrepared:
		return beginNativeRestoreMove(ctx, state, capability, tree, cursor, data.EngineTransitionPreserveIntent, enginePreserveOriginal)
	case data.EngineTransitionOriginalPreserved:
		return beginNativeRestoreMove(ctx, state, capability, tree, cursor, data.EngineTransitionPromoteIntent, enginePromoteTarget)
	case data.EngineTransitionPreserveIntent:
		return settleNativeRestoreMove(ctx, state, capability, tree, cursor, enginePreserveOriginal, data.EngineTransitionOriginalPreserved)
	case data.EngineTransitionPromoteIntent:
		return settleNativeRestoreMove(ctx, state, capability, tree, cursor, enginePromoteTarget, data.EngineTransitionTargetPromoted)
	case data.EngineTransitionTargetPromoted:
		observed, err := tree.inspect(ctx)
		if err != nil {
			return err
		}
		_, err = state.AdvanceNativeRestoreTransition(ctx, capability, data.EngineTransitionActivationIntent, observed)
		return err
	default:
		return errEngineSource
	}
}

func beginNativeRestoreMove(ctx context.Context, state *store.Store, capability *store.NativeRestoreTransition, tree *engineTransitionTree, cursor data.EngineTransitionCursor, intent data.EngineTransitionStage, movement engineTransitionMove) error {
	observed, err := tree.inspect(ctx)
	if err != nil {
		return err
	}
	retained, err := state.AdvanceNativeRestoreTransition(ctx, capability, intent, observed)
	if err != nil {
		return err
	}
	if err = tree.move(ctx, retained, movement); err != nil {
		return err
	}
	return nil
}

func settleNativeRestoreMove(ctx context.Context, state *store.Store, capability *store.NativeRestoreTransition, tree *engineTransitionTree, cursor data.EngineTransitionCursor, movement engineTransitionMove, completed data.EngineTransitionStage) error {
	if err := tree.settle(ctx, cursor, movement); err != nil {
		return err
	}
	observed, err := tree.inspect(ctx)
	if err != nil {
		return err
	}
	_, err = state.AdvanceNativeRestoreTransition(ctx, capability, completed, observed)
	return err
}
