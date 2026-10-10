//go:build linux

package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type taskLauncher func(context.Context, systemd.OperationID) error

func (f taskLauncher) Launch(ctx context.Context, id systemd.OperationID) error { return f(ctx, id) }

type taskStore struct{ *store.Store }

func (taskStore) AcquireHostLock(context.Context) (ops.Lock, error) {
	return nil, errors.New("read-only task attempted host locking")
}

func taskReceipt(id string) json.RawMessage {
	receipt := restore.Receipt{OperationID: id, Source: restore.RestoreSource{Kind: restore.LitestreamLTX, LTX: &restore.LTXSource{Recoverability: true, BindingID: "b1", Epoch: "e1", TXID: 7}}, ToolVersion: restore.LitestreamVersion, RequestedTXID: 7, RecoveredTXID: 7, ObservedAt: time.Now().UTC(), Schema: restore.SchemaObservation{State: restore.VerifiedSchema, Marker: "v1", CatalogSHA256: strings.Repeat("a", 64)}, LossWindow: restore.LossWindow{State: restore.LossUnknown, Reason: "No independent last-commit coverage proof; asynchronous replication may lose recent writes."}, IntegrityCheck: "passed", ForeignKeyCheck: "passed", InvariantCheck: "passed", PositionEvidence: "remote_dry_run_and_restore"}
	raw, _ := json.Marshal(receipt)
	return raw
}

func TestDetachedTaskReceiptBeyondObserverDeadline(t *testing.T) {
	dir := t.TempDir()
	state, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	done := make(chan error, 1)
	entered := make(chan struct{})
	runner := jobs.Runner{Store: taskStore{state}, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.RestoreTest: func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		close(entered)
		timer := time.NewTimer(16 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
		return taskReceipt(op.ID), nil
	}}}
	service := jobs.Service{Store: taskStore{state}, Requester: "operator", Launcher: taskLauncher(func(ctx context.Context, id systemd.OperationID) error {
		worker, stop := context.WithTimeout(context.WithoutCancel(ctx), systemd.JobRuntimeLimit)
		go func() { defer stop(); done <- runner.Run(worker, id.String()) }()
		return nil
	})}
	observer, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	accepted, err := service.Submit(observer, ops.Intent{Kind: ops.RestoreTest, App: "example", SecretRef: "immutable-reference"}, "request1")
	if err != nil || accepted.Status != "accepted" {
		cancel()
		t.Fatalf("acceptance: %v", err)
	}
	<-entered
	<-observer.Done()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	status, err := service.Operation(context.Background(), accepted.OperationID, 0)
	if err != nil || status.Operation.State != ops.Succeeded || status.Outcome == nil {
		t.Fatalf("16-second receipt lost: %+v %v", status, err)
	}
	wire, err := json.Marshal(result.Success("brine host operation", status))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := dispatch.DecodeResponse(wire, "operation")
	if err != nil || decoded.Data.(jobs.Status).Outcome == nil {
		t.Fatal("typed receipt lost at transport boundary", err)
	}
	receipt, err := ops.DecodeTaskReceipt(status.Operation, status.Outcome.Receipt)
	if err != nil || receipt.(restore.Receipt).OperationID != accepted.OperationID {
		t.Fatal("wrong terminal receipt", err)
	}
	if err := runner.Run(context.Background(), accepted.OperationID); err == nil {
		t.Fatal("completed task ran twice")
	}
	readonly, err := store.OpenReadOnly(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readonly.Close() }()
	durable, err := readonly.ReadTaskOutcome(context.Background(), accepted.OperationID)
	if err != nil || durable == nil || string(durable.Receipt) != string(status.Outcome.Receipt) {
		t.Fatal("terminal receipt not durable", err)
	}
}
