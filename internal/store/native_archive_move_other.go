//go:build !linux

package store

import (
	"context"
	"github.com/ShaulLavo/brine/internal/archive"
)

func (s *Store) checkNativeArchiveMoveObservation(context.Context, archive.Plan, archive.NativeTransitionTree, NativeArchiveTreeObservation) error {
	return ErrConflict
}
