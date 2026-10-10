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
	// Resolution owns new effects, but an uncleared intent can belong to any
	// source in its verified receipt chain. Clear those owners too, both before
	// unit compensation and at settled terminals. Unknown outcomes retain the
	// remaining evidence and cannot authorize a restored writer or success.
	for _, owner := range append([]string{x.id}, x.writerStartOwners...) {
		if err := x.executor.WriterStarts.ClearWriterStart(ctx, owner); err != nil {
			return &Error{Step: "clear_writer_start", Code: "interrupted", State: RecoveryRequired, Cause: err}
		}
	}
	return nil
}

// PersistentData performs journaled preparation and independently inspects its
// complete artifact/binding/activation outcome. A running process is neither a
// complete preparation proof nor evidence of remote durability.
type PersistentData interface {
	PreparePersistent(context.Context, string, plan.Plan, policy.Desired) error
	PersistentPrepared(context.Context, string, plan.Plan, policy.Desired) (bool, error)
}

func (x *execution) preparePersistent(ctx context.Context) error {
	if x.desired.Stateless() {
		return nil
	}
	if x.executor.PersistentData == nil {
		return &Error{Step: "prepare_data", Code: "writer_permit_refused"}
	}
	if err := x.executor.PersistentData.PreparePersistent(ctx, x.id, x.plan, x.desired); err != nil {
		// Publication, binding commit and activation are separate effects. Any
		// adapter error can leave a prefix applied; inspect it instead of replaying.
		return &Error{Step: "prepare_data", Code: "interrupted", Cause: err}
	}
	return nil
}
