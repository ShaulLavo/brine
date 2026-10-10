//go:build linux

package host

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

func initializationJobFixture(t *testing.T, requester datainit.Requester) (*store.Store, datainit.Service, datainit.Plan) {
	t.Helper()
	state, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	hash := "sha256:" + strings.Repeat("a", 64)
	definition := data.SchemaDefinition{Database: "main", Marker: "v1", CatalogSHA256: "688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"}
	engine := datainit.Service{Journal: state, Requester: requester, Authorize: func(context.Context) error { return nil }, Lock: func(context.Context) (func(), error) { return func() {}, nil }, Quiesce: func(context.Context, datainit.Plan) (func(), error) { return func() {}, nil }, PrepareRestorePoint: func(context.Context, datainit.Plan, datainit.Operation) (datainit.VerifiedRestorePoint, error) {
		return datainit.VerifiedRestorePoint{}, nil
	}, Facts: func(context.Context, datainit.Request) (datainit.Facts, error) {
		return datainit.Facts{Plan: datainit.Plan{ReplicaEpoch: data.ReplicaEpochID(strings.Repeat("4", 32)), RemotePrefix: "epochs/fixture", Bounds: data.InitializationBounds{MaxBackupAgeSeconds: 300, MaxRestoreTestAgeSeconds: 300, RecoveryWindowSeconds: 3600}, PolicyHash: hash, TargetHash: hash, DesiredHash: hash, Database: data.DatabaseBinding{DatabaseID: data.DatabaseID(strings.Repeat("1", 32)), IncarnationID: data.AppIncarnationID(strings.Repeat("2", 32)), Name: "main", Root: "/unused", RelativeDirectory: "fixture", MountPath: "/data", Filename: "app.db", ReplicaBindingID: data.ReplicaBindingID(strings.Repeat("3", 32))}, Definition: definition}, Initializer: data.SchemaInitializer{Definition: definition, Statements: []string{"CREATE TABLE t(x TEXT)"}}, Observation: data.SchemaObservation{State: data.AllocatedEmpty}}, nil
	}}
	p, err := engine.Plan(context.Background(), datainit.Request{App: "example", FirstReleasePlan: hash, Artifact: hash})
	if err != nil {
		t.Fatal(err)
	}
	return state, engine, p
}

type initializationLauncher struct {
	launch func(systemd.OperationID) error
}

func (l initializationLauncher) Launch(_ context.Context, id systemd.OperationID) error {
	return l.launch(id)
}

func TestDataInitializationJobFinishesAfterSixteenSecondsAndRemainsObservable(t *testing.T) {
	requester, err := datainit.AgentRequester("deploy:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	state, engine, p := initializationJobFixture(t, requester)
	finished := make(chan error, 1)
	var executions atomic.Int32
	runner := jobs.Runner{Store: state, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.DataInitApply: func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		executions.Add(1)
		timer := time.NewTimer(16 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
		return json.Marshal(datainit.Operation{ID: strings.Repeat("5", 32), PlanID: op.SecretRef, Fence: data.FenceID(strings.Repeat("6", 32)), State: "succeeded"})
	}}}
	tasks := jobs.Service{Store: state, Requester: requester.String(), Launcher: initializationLauncher{launch: func(id systemd.OperationID) error {
		go func() { finished <- runner.Run(context.Background(), id.String()) }()
		return nil
	}}}
	backend := initializationOperations{engine: engine, tasks: tasks}
	observer, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	accepted, err := backend.Apply(observer, p.Request.App, p.ID)
	if err != nil || accepted.Status != "accepted" {
		t.Fatal("initialization was not detached", err)
	}
	select {
	case err = <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("detached init failed to finish")
	}
	if observer.Err() != context.DeadlineExceeded {
		t.Fatal("test did not outlive the observer budget")
	}
	status, err := tasks.Operation(context.Background(), accepted.OperationID, 0)
	if err != nil || status.Operation.State != ops.Succeeded || status.Outcome == nil {
		t.Fatal("finished init lost its status", err)
	}
	receipt, err := ops.DecodeTaskReceipt(status.Operation, status.Outcome.Receipt)
	initialized, ok := receipt.(datainit.Operation)
	if err != nil || !ok || initialized.PlanID != p.ID || initialized.State != "succeeded" {
		t.Fatal("initialization outcome lost", err)
	}
	repeated, err := backend.Apply(context.Background(), p.Request.App, p.ID)
	if err != nil || repeated.OperationID != accepted.OperationID || executions.Load() != 1 {
		t.Fatal("accepted task was replayed", err)
	}
}

func TestInitializationJobReconstructsOnlyMatchingServerAuthority(t *testing.T) {
	agent, err := datainit.AgentRequester("deploy:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, requester := range []datainit.Requester{agent, datainit.LocalOperatorRequester()} {
		state, _, p := initializationJobFixture(t, requester)
		// run-op's ambient requester is deliberately unrelated to the accepted job.
		base := Service{Store: state, Requester: "local-operator", Policy: &fakePolicy{p: pol}}
		job := ops.Operation{Kind: ops.DataInitApply, App: p.Request.App, SecretRef: p.ID, Requester: requester.String()}
		reconstructed, err := initializationTaskEngine(context.Background(), base, "/unused", job)
		if err != nil || reconstructed.Requester != requester {
			t.Fatal("immutable authority lost", err)
		}
		err = reconstructed.Authorize(context.Background())
		if requester.IsLocalOperator() {
			if err != nil || !reconstructed.Requester.IsLocalOperator() {
				t.Fatal("local job lost operator authority", err)
			}
		} else if err == nil || reconstructed.Requester.IsLocalOperator() {
			t.Fatal("remote job acquired ambient operator authority")
		}
		foreign := job
		foreign.Requester = agent.String()
		if !requester.IsLocalOperator() {
			foreign.Requester = datainit.LocalOperatorRequester().String()
		}
		if _, err = initializationTaskEngine(context.Background(), base, "/unused", foreign); err == nil {
			t.Fatal("foreign job reused a requester identity")
		}
	}
}
