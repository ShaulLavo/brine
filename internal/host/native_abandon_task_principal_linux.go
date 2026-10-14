//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

func nativeAbandonTaskService(ctx context.Context, service Service, op ops.Operation) (Service, error) {
	if service.Store == nil || op.Kind != ops.NativePrepareAbandon || op.State != ops.Preflight {
		return Service{}, result.New(result.PolicyRefused, nil)
	}
	recorded, err := service.Store.GetOperation(ctx, op.ID)
	if err != nil || recorded.State != ops.Preflight || recorded.Kind != op.Kind || recorded.App != op.App || recorded.SecretRef != op.SecretRef || recorded.Requester != op.Requester {
		return Service{}, result.New(result.PolicyRefused, err)
	}
	input, err := service.Store.ReadNativeTaskInput(ctx, op.ID)
	if err != nil || input.Kind != ops.NativePrepareAbandon || input.Requester.String() != op.Requester {
		return Service{}, result.New(result.PolicyRefused, err)
	}
	service.Requester = input.Requester.String()
	if err := service.Authorize(ctx, dispatch.Mutating); err != nil {
		return Service{}, err
	}
	return service, nil
}
