//go:build !linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/result"
)

func Open(context.Context, string) (*Runtime, error) {
	return nil, result.New(result.DependencyMissing, nil)
}
