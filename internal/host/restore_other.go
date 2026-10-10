//go:build !linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
)

type unavailableRestore struct{}

func newRestoreTests(string) dispatch.RestoreTestOperations { return unavailableRestore{} }
func (unavailableRestore) Test(context.Context, dispatch.RestoreTestArgs) (jobs.Accepted, error) {
	return jobs.Accepted{}, result.New(result.DependencyMissing, nil)
}
