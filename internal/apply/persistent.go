package apply

import (
	"context"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

// WriterStarts binds durable plan-bound startup intent under the host mutation
// lock. The host adapter must freshly observe candidate schema/fences; the
// read-only startup gate also requires the operation's active transient unit.
type WriterStarts interface {
	BindWriterStart(context.Context, string, plan.Plan, policy.Desired) error
	ClearWriterStart(context.Context, string) error
}

func (x *execution) startWriter(ctx context.Context) error {
	if !x.desired.Stateless() {
		if x.executor.WriterStarts == nil {
			return &Error{Step: "start_unit", Code: "writer_permit_refused"}
		}
		if err := x.executor.WriterStarts.BindWriterStart(ctx, x.id, x.plan, x.desired); err != nil {
			return &Error{Step: "start_unit", Code: "writer_permit_refused", Cause: err}
		}
	}
	x.started = true
	return x.executor.Systemd.Start(ctx, x.service)
}

func (x *execution) clearWriterStart(ctx context.Context) error {
	if x.desired.Stateless() {
		return nil
	}
	if x.executor.WriterStarts == nil {
		return &Error{Step: "clear_writer_start", Code: "interrupted", State: RecoveryRequired}
	}
	if err := x.executor.WriterStarts.ClearWriterStart(ctx, x.id); err != nil {
		return &Error{Step: "clear_writer_start", Code: "interrupted", State: RecoveryRequired, Cause: err}
	}
	return nil
}
