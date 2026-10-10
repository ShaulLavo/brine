//go:build !linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/result"
)

func diagnosticMinimumFreeDiskBytes(context.Context) (uint64, error) {
	return 0, result.New(result.DependencyMissing, nil)
}
