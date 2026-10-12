//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
)

// relocateEngineTransitionTree executes only newly journaled moves. A retained
// pending move is adopted through observed layout and directory fsync; a pending
// move whose effect cannot be positively observed stays fenced for resolution.
func relocateEngineTransitionTree(ctx context.Context, control engineTransitionControl, tree *engineTransitionTree) error {
	if control.state == nil || tree == nil || !tree.active() {
		return errEngineSource
	}
	for {
		input, cursor, err := control.read(ctx)
		if err != nil || input.SHA256() != tree.input.SHA256() {
			return errEngineSource
		}
		if cursor.Stage == data.EngineTransitionActivationIntent || cursor.Stage == data.EngineTransitionActivated || cursor.Stage == data.EngineTransitionFinishIntent {
			return nil
		}
		if err = relocateEngineTransitionStep(ctx, control, tree, cursor); err != nil {
			return err
		}
	}
}

func relocateEngineTransitionStep(ctx context.Context, control engineTransitionControl, tree *engineTransitionTree, cursor data.EngineTransitionCursor) error {
	switch cursor.Stage {
	case data.EngineTransitionPrepared:
		return beginEngineTransitionMove(ctx, control, tree, data.EngineTransitionPreserveIntent, enginePreserveOriginal)
	case data.EngineTransitionOriginalPreserved:
		return beginEngineTransitionMove(ctx, control, tree, data.EngineTransitionPromoteIntent, enginePromoteTarget)
	case data.EngineTransitionPreserveIntent:
		return settleEngineTransitionMove(ctx, control, tree, cursor, enginePreserveOriginal, data.EngineTransitionOriginalPreserved)
	case data.EngineTransitionPromoteIntent:
		return settleEngineTransitionMove(ctx, control, tree, cursor, enginePromoteTarget, data.EngineTransitionTargetPromoted)
	case data.EngineTransitionTargetPromoted:
		observed, err := tree.inspect(ctx)
		if err != nil {
			return err
		}
		_, err = control.advance(ctx, data.EngineTransitionActivationIntent, observed)
		return err
	default:
		return errEngineSource
	}
}

func beginEngineTransitionMove(ctx context.Context, control engineTransitionControl, tree *engineTransitionTree, intent data.EngineTransitionStage, movement engineTransitionMove) error {
	observed, err := tree.inspect(ctx)
	if err != nil {
		return err
	}
	retained, err := control.advance(ctx, intent, observed)
	if err != nil {
		return err
	}
	if err = tree.move(ctx, retained, movement); err != nil {
		return err
	}
	return nil
}

func settleEngineTransitionMove(ctx context.Context, control engineTransitionControl, tree *engineTransitionTree, cursor data.EngineTransitionCursor, movement engineTransitionMove, completed data.EngineTransitionStage) error {
	if err := tree.settle(ctx, cursor, movement); err != nil {
		return err
	}
	observed, err := tree.inspect(ctx)
	if err != nil {
		return err
	}
	_, err = control.advance(ctx, completed, observed)
	return err
}

type engineTransitionControl struct {
	state       *store.Store
	publication *store.PublicationTransition
	restoration *store.NativeRestoreTransition
}

func (c engineTransitionControl) read(ctx context.Context) (data.EngineTransition, data.EngineTransitionCursor, error) {
	if c.state == nil || (c.publication == nil) == (c.restoration == nil) {
		return data.EngineTransition{}, data.EngineTransitionCursor{}, errEngineSource
	}
	if c.publication != nil {
		view, err := c.state.ReadPublicationTransition(ctx, c.publication)
		return view.Input, view.Cursor, err
	}
	view, err := c.state.ReadNativeRestoreTransition(ctx, c.restoration)
	return view.Input, view.Cursor, err
}
func (c engineTransitionControl) advance(ctx context.Context, next data.EngineTransitionStage, observed data.EngineLayoutObservation) (data.EngineTransitionCursor, error) {
	if c.state == nil || (c.publication == nil) == (c.restoration == nil) {
		return data.EngineTransitionCursor{}, errEngineSource
	}
	if c.publication != nil {
		return c.state.AdvancePublicationTransition(ctx, c.publication, next, observed)
	}
	return c.state.AdvanceNativeRestoreTransition(ctx, c.restoration, next, observed)
}
func relocatePublicationTree(ctx context.Context, state *store.Store, capability *store.PublicationTransition, tree *engineTransitionTree) error {
	return relocateEngineTransitionTree(ctx, engineTransitionControl{state: state, publication: capability}, tree)
}
