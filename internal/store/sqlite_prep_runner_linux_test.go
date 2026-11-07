//go:build linux

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

type prepHandler func(context.Context, sqliteprep.Task) (json.RawMessage, error)

func (fx *prepFixture) run(kind sqliteprep.TaskKind, preparation, supersedes string, handler prepHandler) (ops.Operation, error) {
	_, _, op := fx.operation(kind, preparation, supersedes)
	runner := jobs.Runner{Store: fx.s, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.Kind(kind): func(ctx context.Context, current ops.Operation) (json.RawMessage, error) {
		return handler(ctx, sqliteprep.Task{OperationID: current.ID, Kind: kind, Requester: current.Requester, App: current.App, InputSHA256: current.SecretRef})
	}}}
	err := runner.Run(context.Background(), op.ID)
	return op, err
}

func (fx *prepFixture) prepareHandler(host *prepHost, journal sqliteprep.Journal, preparation string) prepHandler {
	return func(ctx context.Context, task sqliteprep.Task) (json.RawMessage, error) {
		in := sqliteprep.Input{Kind: sqliteprep.PrepareTask, PreparationID: preparation, App: fx.app, DatabaseID: fx.reserved.Database.DatabaseID}
		result, err := fx.service(host, journal).Prepare(ctx, task, in)
		if err != nil {
			return nil, err
		}
		return sqliteprep.Encode(result.Receipt)
	}
}

func (fx *prepFixture) outcome(id string) *ops.TaskOutcome {
	outcome, err := fx.s.ReadSQLiteTaskOutcome(context.Background(), id)
	if err != nil {
		fx.t.Fatal(err)
	}
	return outcome
}

func TestSQLiteRunnerCompletesThroughJoinedReadback(t *testing.T) {
	fx := newPrepFixture(t)
	op, err := fx.run(sqliteprep.PrepareTask, prepID("1"), "", fx.prepareHandler(newPrepHost(fx, true), nil, prepID("1")))
	if err != nil || fx.state(op.ID) != ops.Succeeded || fx.outcome(op.ID) == nil {
		t.Fatalf("runner success: %v", err)
	}
}

// A handler whose acknowledgement is lost after the atomic completion, or
// that returns different receipt bytes, never overwrites the stored outcome.
func TestSQLiteRunnerNeverOverwritesKnownCompletion(t *testing.T) {
	fx := newPrepFixture(t)
	succeed := fx.prepareHandler(newPrepHost(fx, true), nil, prepID("1"))
	op, err := fx.run(sqliteprep.PrepareTask, prepID("1"), "", func(ctx context.Context, task sqliteprep.Task) (json.RawMessage, error) {
		if _, err := succeed(ctx, task); err != nil {
			return nil, err
		}
		return nil, errPrepFault
	})
	stored := fx.outcome(op.ID)
	if !errors.Is(err, errPrepFault) || fx.state(op.ID) != ops.Succeeded || stored == nil || len(stored.Receipt) == 0 {
		t.Fatalf("lost acknowledgement rewrote success: %v %s", err, fx.state(op.ID))
	}
	other := newPrepFixture(t)
	inner := other.prepareHandler(newPrepHost(other, true), nil, prepID("1"))
	op, err = other.run(sqliteprep.PrepareTask, prepID("1"), "", func(ctx context.Context, task sqliteprep.Task) (json.RawMessage, error) {
		raw, err := inner(ctx, task)
		return append(bytes.Clone(raw), ' '), err
	})
	if err == nil || other.state(op.ID) != ops.Succeeded {
		t.Fatalf("mismatched receipt accepted or rewritten: %v", err)
	}
}

func TestSQLiteRunnerFailsOnlyProtectedUnclaimedRefusal(t *testing.T) {
	fx := newPrepFixture(t)
	refusal := &sqliteprep.Refusal{Code: sqliteprep.LocalUncertain}
	op, err := fx.run(sqliteprep.PrepareTask, prepID("1"), "", func(context.Context, sqliteprep.Task) (json.RawMessage, error) {
		return nil, refusal
	})
	if !errors.As(err, &refusal) || fx.state(op.ID) != ops.Failed || fx.count("SELECT count(*) FROM data_sqlite_task_settlements WHERE operation_id=? AND code='local_uncertainty'", op.ID) != 1 {
		t.Fatalf("unclaimed refusal: %v %s", err, fx.state(op.ID))
	}
	// A refusal label after a real claim is not proof that nothing happened.
	journal := &faultJournal{SQLitePreparationJournal: fx.journal(), before: sqliteprep.Verified, settle: true}
	claimed := fx.prepareHandler(newPrepHost(fx, true), journal, prepID("2"))
	op, err = fx.run(sqliteprep.PrepareTask, prepID("2"), "", func(ctx context.Context, task sqliteprep.Task) (json.RawMessage, error) {
		_, err := claimed(ctx, task)
		return nil, errors.Join(&sqliteprep.Refusal{Code: sqliteprep.LocalUncertain}, err)
	})
	if err == nil || fx.state(op.ID) != ops.RecoveryRequired || fx.count("SELECT count(*) FROM data_sqlite_task_settlements WHERE operation_id=? AND code='effect_unknown'", op.ID) != 1 {
		t.Fatalf("claimed work settled as failed: %v %s", err, fx.state(op.ID))
	}
	assertSQLiteFenceProtected(t, fx, fx.preparation(prepID("2")).intent.Fence)
	op, err = fx.run(sqliteprep.PrepareTask, prepID("3"), "", func(context.Context, sqliteprep.Task) (json.RawMessage, error) {
		return nil, nil
	})
	if err == nil || fx.state(op.ID) != ops.RecoveryRequired {
		t.Fatalf("unsettled empty success: %v %s", err, fx.state(op.ID))
	}
}
