//go:build !linux

package store

import (
	"context"
	"os"

	"github.com/ShaulLavo/brine/internal/nativecapture"
)

func checkPublicationCheckpointMembers(context.Context, *os.Root, nativecapture.Checkpoint) error {
	return ErrConflict
}
