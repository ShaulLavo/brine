package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
)

type metadataStatus struct{ dir string }

func (metadataStatus) Apply(context.Context, string, string) (jobs.Accepted, error) {
	return jobs.Accepted{}, result.New(result.DispatchOperationRefused, nil)
}
func (s metadataStatus) Operation(ctx context.Context, id string, cursor uint64) (jobs.Status, error) {
	state, err := openState(ctx, s.dir, stateReadOnly, nil)
	if err != nil {
		return jobs.Status{}, err
	}
	defer func() { _ = state.Close() }()
	return (jobs.Service{Store: state.Store}).Operation(ctx, id, cursor)
}
