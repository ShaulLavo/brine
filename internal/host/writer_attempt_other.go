//go:build !linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/replication"
)

func WriterAttempt(context.Context, string) error { return replication.ErrPermit }
