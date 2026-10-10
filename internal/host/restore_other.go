//go:build !linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/result"
)

type unavailableRestore struct{}

func newRestoreTests(string) dispatch.RestoreTestOperations { return unavailableRestore{} }
func (unavailableRestore) Test(context.Context, dispatch.RestoreTestArgs) (restore.Receipt, error) {
	return restore.Receipt{}, result.New(result.DependencyMissing, nil)
}
