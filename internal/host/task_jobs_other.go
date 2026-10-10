//go:build !linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/result"
)

type DetachedRunner struct{}

func (DetachedRunner) Run(context.Context, string) error {
	return result.New(result.DependencyMissing, nil)
}
