//go:build linux

package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	root := t.TempDir()
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	} // #nosec G302 -- Private fixture root requires owner traversal.
	reserved, err := state.ReserveDatabase(context.Background(), store.DataReservation{App: "example", PolicyHash: hash, Database: data.Database{Name: "main", PersistentRoot: data.PersistentRoot(root), MountPath: "/data", Filename: "app.db", BackupDestination: "primary"}, Destination: data.Destination{Reference: "primary", Endpoint: "https://storage.example", Region: "region-1", Bucket: "backups", BasePrefix: "brine", CredentialRef: "primary"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, reserved.Database.RelativeDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	allocation, err := data.CaptureAllocation(reserved.Database)
	if err != nil {
		t.Fatal("capture allocation", err)
	}
	definition := data.SchemaDefinition{Database: "main", Marker: "v1", CatalogSHA256: "688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"}
	engine := datainit.Service{Journal: state, Requester: requester, Authorize: func(context.Context) error { return nil }, Lock: func(context.Context) (func(), error) { return func() {}, nil }, Quiesce: func(context.Context, datainit.Plan) (func(), error) { return func() {}, nil }, PrepareRestorePoint: func(context.Context, datainit.Plan, datainit.Operation) (datainit.VerifiedRestorePoint, error) {
		return datainit.VerifiedRestorePoint{}, nil
	}, Facts: func(ctx context.Context, _ datainit.Request) (datainit.Facts, error) {
		return datainit.Facts{Plan: datainit.Plan{ReplicaEpoch: reserved.Replica.EpochID, RemotePrefix: strings.TrimSuffix(reserved.Replica.RemotePrefix, "/"), Bounds: data.InitializationBounds{MaxBackupAgeSeconds: 300, MaxRestoreTestAgeSeconds: 300, RecoveryWindowSeconds: 3600}, PolicyHash: hash, TargetHash: hash, DesiredHash: hash, Database: reserved.Database, Definition: definition}, Initializer: data.SchemaInitializer{Definition: definition, Statements: []string{"CREATE TABLE t(x TEXT)"}}, Allocation: &allocation, Observation: data.ObserveSchemaWithAllocation(ctx, reserved.Database, []data.SchemaDefinition{definition}, &allocation)}, nil
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
		accepted, _, err := state.CreateOperation(context.Background(), ops.Intent{Kind: ops.DataInitApply, App: p.Request.App, SecretRef: p.ID}, requester.String(), "authority-retained")
		if err != nil {
			t.Fatal(err)
		}
		job, err := state.GetOperation(context.Background(), accepted.ID)
		if err != nil {
			t.Fatal(err)
		}
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

func TestInitializationRemoteJobCannotAcceptOrReuseLocalAuthority(t *testing.T) {
	state, engine, p := initializationJobFixture(t, datainit.LocalOperatorRequester())
	agent, err := datainit.AgentRequester("deploy:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	engine.Requester = agent
	tasks := jobs.Service{Store: state, Requester: agent.String(), Launcher: initializationLauncher{launch: func(systemd.OperationID) error { t.Fatal("remote launched a local operator plan"); return nil }}}
	if _, err = (initializationOperations{engine: engine, tasks: tasks}).Apply(context.Background(), p.Request.App, p.ID); err == nil {
		t.Fatal("remote reused a local operator plan")
	}
	engine.Requester = datainit.LocalOperatorRequester()
	if _, err = (initializationOperations{engine: engine, tasks: tasks}).Apply(context.Background(), p.Request.App, p.ID); err == nil {
		t.Fatal("remote job acquired operator authority from ambient composition")
	}
	pending, err := state.ListUnfinished(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatal("refused remote authority wrote a job", err)
	}
}
