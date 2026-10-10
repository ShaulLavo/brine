package cli

import (
	"context"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/systemd"
)

// waitTask leaves each transport call's 15-second budget unchanged. Only the
// observer has an overall job-runtime bound; its departure cannot cancel work.
// A no-wait caller receives the accepted operation ID and can query status later.
func (f operationFlags) waitTask(ctx context.Context, deps Dependencies, accepted jobs.Accepted, kind ops.Kind, noWait bool) (any, error) {
	return waitAcceptedTask(ctx, accepted, kind, noWait, func(ctx context.Context, args dispatch.OperationArgs) (jobs.Status, error) {
		response, err := f.call(ctx, deps, "operation", args)
		if err != nil {
			return jobs.Status{}, err
		}
		if !response.OK {
			if response.Error == nil || !result.KnownCode(response.Error.Code) {
				return jobs.Status{}, result.New(result.TransportInvalidResponse, nil)
			}
			return jobs.Status{}, result.New(response.Error.Code, nil)
		}
		status, ok := response.Data.(jobs.Status)
		if !ok {
			return jobs.Status{}, result.New(result.TransportInvalidResponse, nil)
		}
		return status, nil
	})
}

func waitAcceptedTask(ctx context.Context, accepted jobs.Accepted, kind ops.Kind, noWait bool, readStatus func(context.Context, dispatch.OperationArgs) (jobs.Status, error)) (any, error) {
	if accepted.Status != "accepted" || !jobs.ValidID(accepted.OperationID) || !kind.IsTask() {
		return nil, result.New(result.TransportInvalidResponse, nil)
	}
	if noWait {
		return accepted, nil
	}
	ctx, cancel := context.WithTimeout(ctx, systemd.JobRuntimeLimit+time.Minute)
	defer cancel()
	var cursor uint64
	for {
		status, err := readStatus(ctx, dispatch.OperationArgs{OperationID: accepted.OperationID, AfterCursor: cursor})
		if err != nil {
			return nil, err
		}
		if status.Operation.ID != accepted.OperationID || status.Operation.Kind != kind || status.NextCursor < cursor {
			return nil, result.New(result.TransportInvalidResponse, nil)
		}
		cursor = status.NextCursor
		if status.Operation.State.IsTerminal() {
			if status.Outcome == nil && status.Operation.State != ops.Succeeded {
				if status.Operation.State == ops.RecoveryRequired {
					return nil, result.New(result.RecoveryRequired, nil)
				}
				return nil, result.New(result.InternalError, nil)
			}
			if status.Outcome == nil || !status.Outcome.Valid(status.Operation) {
				return nil, result.New(result.TransportInvalidResponse, nil)
			}
			if status.Outcome.Error != "" {
				return nil, result.New(status.Outcome.Error, nil)
			}
			return ops.DecodeTaskReceipt(status.Operation, status.Outcome.Receipt)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, result.New(result.Interrupted, ctx.Err())
		case <-timer.C:
		}
	}
}
