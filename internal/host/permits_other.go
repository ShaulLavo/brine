//go:build !linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/replication"
)

type PermitRuntime struct{ Reader replication.LaunchReader }

func (*PermitRuntime) Close() error                       { return nil }
func OpenPermits(context.Context) (*PermitRuntime, error) { return nil, replication.ErrPermit }
