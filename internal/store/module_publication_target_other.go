//go:build !linux

package store

import (
	"context"
	"github.com/ShaulLavo/brine/internal/data"
)

func checkPublicationTargetAnchors(context.Context, *PublicationTargetPreparation, PublicationTargetObservation) error {
	return ErrConflict
}

func checkPublicationTransitionAnchors(context.Context, *PublicationTransition, data.EngineLayoutObservation) error {
	return ErrConflict
}
