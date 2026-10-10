//go:build !linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/replication"
)

func OpenWriterAttempt(context.Context) (*WriterAttemptRuntime, error) {
	return nil, replication.ErrPermit
}
