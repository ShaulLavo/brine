//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/store"
)

// The connected composition uses the real verifier and task store, without
// opening enrolled credentials, inventory, mutation locks or an external host.
func TestRestoreTaskDisposesVerifiedWorkspaces(t *testing.T) {
	for _, kind := range []restore.SourceKind{restore.SQLiteSnapshot, restore.LitestreamLTX} {
		t.Run(string(kind), func(t *testing.T) { testRestoreTaskDisposesWorkspaces(t, kind) })
	}
}

func testRestoreTaskDisposesWorkspaces(t *testing.T, kind restore.SourceKind) {
	dir := t.TempDir()
	state, err := store.OpenContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	engine, request, source := restoreTaskFixture(t, dir)
	if kind == restore.SQLiteSnapshot {
		request.Source = restoreTaskSnapshot(t, engine, source)
	}
	handler := restoreTaskHandlerWithCheck(state, func(ctx context.Context, _ dispatch.RestoreTestArgs, id string) (restore.SQLiteReceipt, error) {
		request.OperationID = id
		return checkSQLiteRestore(ctx, engine, request)
	})
	runner := jobs.Runner{Store: state, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.RestoreTest: handler}}
	reference, err := state.SaveRestoreTaskInput(context.Background(), "operator", dispatch.RestoreTestArgs{App: "example", Database: "main", TXID: "0000000000000007"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, key := range []string{"restore-first", "restore-second"} {
		op, _, err := state.CreateOperation(context.Background(), ops.Intent{Kind: ops.RestoreTest, App: "example", SecretRef: reference}, "operator", key)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, op.ID)
		if err := runner.Run(context.Background(), op.ID); err != nil {
			t.Fatal(err)
		}
	}
	readonly, err := store.OpenReadOnly(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readonly.Close() }()
	for _, id := range ids {
		assertRestoreTaskReceipt(t, readonly, id)
	}
	// #nosec G304 -- Reads only the source fixture in this test-owned temporary state directory.
	got, err := os.ReadFile(filepath.Join(dir, "source.sqlite"))
	if err != nil || sha256.Sum256(got) != sha256.Sum256(source) {
		t.Fatal("source database changed", err)
	}
	entries, err := os.ReadDir(engine.Root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("settled restore tasks retain disposable database bytes: %v, %v", entries, err)
	}
	t.Logf("Two verified copies of %d bytes each; settled disposable bytes: 0", len(source))
}

func assertRestoreTaskReceipt(t *testing.T, state *store.Store, id string) {
	t.Helper()
	status, err := (jobs.Service{Store: state}).Operation(context.Background(), id, 0)
	if err != nil || status.Operation.State != ops.Succeeded || status.Outcome == nil {
		t.Fatalf("receipt not queryable: %+v %v", status, err)
	}
	receipt, err := ops.DecodeTaskReceipt(status.Operation, status.Outcome.Receipt)
	if err != nil || receipt.(restore.SQLiteReceipt).OperationID != id || receipt.(restore.SQLiteReceipt).DatabasePath != "" {
		t.Fatal("durable metadata receipt lost", err)
	}
}

type restoreTaskCLI struct{ source []byte }

func (c restoreTaskCLI) Execute(_ context.Context, command restore.Command) (restore.CommandResult, error) {
	if command.Args[0] == "version" {
		return restore.CommandResult{Stdout: []byte(restore.LitestreamVersion)}, nil
	}
	var output string
	for i, arg := range command.Args {
		if arg == "-o" {
			output = command.Args[i+1]
		}
	}
	if strings.Contains(strings.Join(command.Args, " "), "-dry-run") {
		return restore.CommandResult{Stdout: []byte(`{"target_path":"` + output + `","replica":"s3","max_txid":"0000000000000007"}`)}, nil
	}
	if err := os.WriteFile(output, c.source, 0600); err != nil {
		return restore.CommandResult{}, err
	}
	return restore.CommandResult{Stdout: []byte(`{"db_path":"` + output + `","replica":"s3","txid":"0000000000000007","integrity_check":"full"}`)}, nil
}

type restoreTaskObserver struct {
	schema restore.SQLiteSchemaObservation
	before func()
}

func (o restoreTaskObserver) Observe(context.Context, *sql.Tx) (restore.SQLiteSchemaObservation, error) {
	if o.before != nil {
		o.before()
	}
	return o.schema, nil
}

func restoreTaskFixture(t *testing.T, dir string) (*restore.SQLiteEngine, restore.SQLiteRequest, []byte) {
	t.Helper()
	sourcePath := filepath.Join(dir, "source.sqlite")
	db, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ddl, err := os.ReadFile("../restore/testdata/fixture.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- Reads the SQLite fixture created above inside t.TempDir.
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(source)
	schema := restore.SQLiteSchemaObservation{State: restore.VerifiedSchema, Marker: "fixture-v1", CatalogSHA256: hex.EncodeToString(digest[:])}
	root := filepath.Join(dir, "restores")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	engine := &restore.SQLiteEngine{Root: root, CLI: restoreTaskCLI{source}, Observer: restoreTaskObserver{schema: schema}, Bindings: restore.BindingReaderFunc(func(context.Context, string, string) (restore.Binding, error) {
		return restore.Binding{ID: "b1", Epoch: "e1", CredentialRef: "c1", Destination: restore.Destination{Endpoint: "https://storage.example", Region: "auto", Bucket: "bucket", Prefix: "epochs/e1", PathStyle: true}}, nil
	}), Credentials: restore.CredentialReaderFunc(func(context.Context, string) (restore.Credentials, error) {
		return restore.Credentials{AccessKey: "test-key", SecretKey: "test-secret"}, nil
	})}
	request := restore.SQLiteRequest{Source: restore.SQLiteSource{Kind: restore.LitestreamLTX, LTX: &restore.LTXSource{BindingID: "b1", Epoch: "e1", TXID: 7, Recoverability: true}}, Budget: time.Minute, ExpectedSchema: schema}
	return engine, request, source
}

func TestRestoreTaskSerializationFailureDisposesOutput(t *testing.T) {
	dir := t.TempDir()
	state, err := store.OpenContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	engine, request, _ := restoreTaskFixture(t, dir)
	handler := restoreTaskHandlerWithCheck(state, func(ctx context.Context, _ dispatch.RestoreTestArgs, id string) (restore.SQLiteReceipt, error) {
		request.OperationID = id
		receipt, err := checkSQLiteRestore(ctx, engine, request)
		receipt.ObservedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		return receipt, err
	})
	op := createRestoreTask(t, state, "serialize-failure")
	runner := jobs.Runner{Store: state, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.RestoreTest: handler}}
	if err := runner.Run(context.Background(), op.ID); err == nil {
		t.Fatal("serialization failure accepted")
	}
	status, err := (jobs.Service{Store: state}).Operation(context.Background(), op.ID, 0)
	if err != nil || status.Operation.State != ops.Failed || status.Outcome == nil || len(status.Outcome.Receipt) != 0 || status.Outcome.Error == "" {
		t.Fatal("serialization failure not durable", err)
	}
	assertRestoreTaskEmpty(t, engine.Root)
}

func createRestoreTask(t *testing.T, state *store.Store, key string) ops.Operation {
	t.Helper()
	reference, err := state.SaveRestoreTaskInput(context.Background(), "operator", dispatch.RestoreTestArgs{App: "example", Database: "main", TXID: "0000000000000007"})
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := state.CreateOperation(context.Background(), ops.Intent{Kind: ops.RestoreTest, App: "example", SecretRef: reference}, "operator", key)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func assertRestoreTaskEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("disposable task bytes retained", entries, err)
	}
}

type restoreTaskInterruptedStore struct {
	*store.Store
	root      string
	cancel    context.CancelFunc
	persisted bool
	t         *testing.T
}

func (s restoreTaskInterruptedStore) CompleteTask(ctx context.Context, id string, state ops.State, outcome ops.TaskOutcome) error {
	assertRestoreTaskEmpty(s.t, s.root)
	s.cancel()
	if ctx.Err() != nil {
		s.t.Error("journal context inherited worker cancellation")
	}
	if s.persisted {
		if err := s.Store.CompleteTask(ctx, id, state, outcome); err != nil {
			return err
		}
	}
	return errors.New("synthetic lost persistence acknowledgement")
}

func TestRestoreTaskInterruptedResultPersistence(t *testing.T) {
	for _, persisted := range []bool{false, true} {
		t.Run(strconv.FormatBool(persisted), func(t *testing.T) { testRestoreTaskInterruptedPersistence(t, persisted) })
	}
}

func testRestoreTaskInterruptedPersistence(t *testing.T, persisted bool) {
	dir := t.TempDir()
	state, err := store.OpenContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	engine, request, _ := restoreTaskFixture(t, dir)
	calls := 0
	handler := restoreTaskHandlerWithCheck(state, func(ctx context.Context, _ dispatch.RestoreTestArgs, id string) (restore.SQLiteReceipt, error) {
		calls++
		request.OperationID = id
		return checkSQLiteRestore(ctx, engine, request)
	})
	op := createRestoreTask(t, state, "interrupted")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interrupted := restoreTaskInterruptedStore{Store: state, root: engine.Root, cancel: cancel, persisted: persisted, t: t}
	runner := jobs.Runner{Store: interrupted, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.RestoreTest: handler}}
	if err := runner.Run(ctx, op.ID); err == nil {
		t.Fatal("lost persistence acknowledgement ignored")
	}
	if err := runner.Run(context.Background(), op.ID); err == nil || calls != 1 {
		t.Fatal("interrupted task repeated verification", err)
	}
	assertRestoreTaskEmpty(t, engine.Root)
	if persisted {
		assertRestoreTaskReceipt(t, state, op.ID)
		return
	}
	status, err := (jobs.Service{Store: state}).Operation(context.Background(), op.ID, 0)
	if err != nil || status.Operation.State != ops.Preflight || status.Outcome != nil {
		t.Fatal("unpersisted task invented success", err)
	}
}

func TestRestoreTaskCancellationSettlesWithoutOutput(t *testing.T) {
	dir := t.TempDir()
	state, err := store.OpenContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	engine, request, _ := restoreTaskFixture(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.Observer = restoreTaskObserver{schema: request.ExpectedSchema, before: cancel}
	handler := restoreTaskHandlerWithCheck(state, func(ctx context.Context, _ dispatch.RestoreTestArgs, id string) (restore.SQLiteReceipt, error) {
		request.OperationID = id
		return checkSQLiteRestore(ctx, engine, request)
	})
	op := createRestoreTask(t, state, "cancel-worker")
	runner := jobs.Runner{Store: state, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.RestoreTest: handler}}
	if err := runner.Run(ctx, op.ID); err == nil {
		t.Fatal("canceled worker returned success")
	}
	status, err := (jobs.Service{Store: state}).Operation(context.Background(), op.ID, 0)
	if err != nil || status.Operation.State != ops.Failed || status.Outcome == nil || status.Outcome.Error == "" {
		t.Fatal("cancellation was not settled", err)
	}
	assertRestoreTaskEmpty(t, engine.Root)
}

func restoreTaskSnapshot(t *testing.T, engine *restore.SQLiteEngine, source []byte) restore.SQLiteSource {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(source); err != nil {
			t.Log("fixture client closed response:", err)
		}
	}))
	t.Cleanup(server.Close)
	engine.AllowHTTPForTests = true
	engine.Bindings = restore.BindingReaderFunc(func(context.Context, string, string) (restore.Binding, error) {
		return restore.Binding{ID: "b1", Epoch: "e1", CredentialRef: "c1", Destination: restore.Destination{Endpoint: server.URL, Region: "auto", Bucket: "bucket", Prefix: "epochs/e1", PathStyle: true}}, nil
	})
	digest := sha256.Sum256(source)
	return restore.SQLiteSource{Kind: restore.SQLiteSnapshot, Snapshot: &restore.SnapshotSource{BindingID: "b1", Epoch: "e1", PointID: "p1", ObjectKey: "epochs/e1/restore-points/p1/snapshot.sqlite", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(source))}}
}
