package store

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

// validateConsumer joins a claim without recursively reading its successor or
// its source preparation. Unsupported reviewed task kinds grant no authority.
func (p sqlitePreparation) validateConsumer(ctx context.Context, q dataQuerier, c sqliteprep.Consumer) error {
	if c.Requester != p.intent.Task.Requester {
		return &IntegrityError{}
	}
	kind := sqliteprep.PrepareTask
	if c.Kind == sqliteprep.AbandonConsumer {
		kind = sqliteprep.AbandonTask
	}
	task := sqliteprep.Task{OperationID: c.OperationID, Kind: kind, Requester: c.Requester, App: c.App, InputSHA256: c.InputSHA256}
	op, in, err := readSQLiteOperation(ctx, q, task)
	if err != nil || in.DatabaseID != p.intent.Scope.DatabaseID {
		return errors.Join(&IntegrityError{}, err)
	}
	switch c.Kind {
	case sqliteprep.SupersedeConsumer:
		return p.validateSuccessorConsumer(ctx, q, c, op, in)
	case sqliteprep.AbandonConsumer:
		return p.validateAbandonConsumer(ctx, q, c, op, in, task)
	default:
		return &IntegrityError{}
	}
}

func (p sqlitePreparation) validateSuccessorConsumer(ctx context.Context, q dataQuerier, c sqliteprep.Consumer, op ops.Operation, in sqliteprep.Input) error {
	if in.Supersedes != p.intent.Input.PreparationID || in.PreparationID != c.SuccessorID || op.State != ops.Preflight && op.State != ops.RecoveryRequired && op.State != ops.Succeeded {
		return &IntegrityError{}
	}
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT canonical FROM data_sqlite_preparations WHERE id=? AND operation_id=? AND database_id=? AND fence_id=? AND supersedes=?", c.SuccessorID, c.OperationID, p.intent.Scope.DatabaseID, p.intent.Fence.ID, p.intent.Input.PreparationID).Scan(&raw)
	if err != nil {
		return errors.Join(&IntegrityError{}, err)
	}
	successor, err := sqliteprep.DecodeIntent(raw)
	expected := sqliteprep.Task{OperationID: c.OperationID, Kind: sqliteprep.PrepareTask, Requester: c.Requester, App: c.App, InputSHA256: c.InputSHA256}
	if err != nil || successor.Task != expected || successor.Input != in || successor.Fence != p.intent.Fence || !sqliteprep.SameLineageScope(p.intent.Scope, successor.Scope) || successor.Scope.PolicySHA256 != c.PolicySHA256 || !successor.ClaimedAt.Equal(c.ClaimedAt) {
		return &IntegrityError{}
	}
	return nil
}

func (p sqlitePreparation) validateAbandonConsumer(ctx context.Context, q dataQuerier, c sqliteprep.Consumer, op ops.Operation, in sqliteprep.Input, task sqliteprep.Task) error {
	if op.State != ops.Succeeded || in.PreparationID != p.intent.Input.PreparationID || in.Supersedes != "" {
		return &IntegrityError{}
	}
	var raw []byte
	if err := q.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", op.ID).Scan(&raw); err != nil {
		return errors.Join(&IntegrityError{}, err)
	}
	outcome, err := ops.DecodeTaskOutcome(op, raw)
	if err != nil {
		return &IntegrityError{}
	}
	receipt, err := sqliteprep.DecodeReceipt(outcome.Receipt)
	fence := p.intent.Fence
	fence.State = data.FenceReleased
	if err != nil || receipt.Task != task || receipt.PreparationID != in.PreparationID || receipt.Outcome != sqliteprep.AbandonedOutcome || receipt.Fence != fence || !receipt.CompletedAt.Equal(c.ClaimedAt) || receipt.PreparedSHA256 != c.PreparedSHA256 {
		return &IntegrityError{}
	}
	if err := p.validateConsumerPoint(c, receipt); err != nil {
		return err
	}
	var count int
	err = q.QueryRowContext(ctx, "SELECT count(*) FROM data_sqlite_consumer_completions WHERE preparation_id=? AND operation_id=? AND fence_id=?", c.PreparationID, c.OperationID, p.intent.Fence.ID).Scan(&count)
	if err != nil || count != 1 {
		return errors.Join(&IntegrityError{}, err)
	}
	return nil
}

func (p sqlitePreparation) validateConsumerPoint(c sqliteprep.Consumer, r sqliteprep.Receipt) error {
	if p.point == nil {
		if c.PreparedSHA256 != "" || r.SourcePointID != "" {
			return &IntegrityError{}
		}
		return nil
	}
	digest, err := sqliteprep.Fingerprint(*p.point)
	if err != nil || digest != c.PreparedSHA256 || r.SourcePointID != p.point.Source.Point.ID {
		return &IntegrityError{}
	}
	return nil
}
