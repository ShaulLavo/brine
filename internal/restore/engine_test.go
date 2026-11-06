package restore

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEngineVerificationUsesTheSQLiteVerifier(t *testing.T) {
	engine, request, _ := setup(t, fixture(t, fixtureSQL))
	receipt, err := VerifySQLite(context.Background(), engine, request)
	if err != nil || !receipt.Valid() || receipt.Source.Kind != SQLiteSnapshot {
		t.Fatal("tagged verification lost SQLite checks or receipt", err)
	}
}

func TestSQLiteReceiptRefusesAnotherEngineTag(t *testing.T) {
	raw, err := os.ReadFile("testdata/sqlite-engine-receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	foreign := strings.Replace(string(raw), `"source":{`, `"source":{"engine":"spacetimedb",`, 1)
	if _, err = DecodeReceipt([]byte(foreign)); err == nil {
		t.Fatal("foreign engine tag consumed a checked SQLite receipt")
	}
}

func TestRestoreMetadataSnapshotDisposesOutput(t *testing.T) {
	engine, request, _ := setup(t, fixture(t, fixtureSQL))
	receipt, err := engine.TestMetadata(context.Background(), request)
	if err != nil || !receipt.Valid() || receipt.DatabasePath != "" {
		t.Fatal("metadata receipt failed", err)
	}
	assertRestoreWorkspaceEmpty(t, engine.Root)
}

func TestRestoreMetadataPreservesActiveGenericConsumer(t *testing.T) {
	engine, request, _ := setup(t, fixture(t, fixtureSQL))
	retained, err := engine.Test(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.TestMetadata(context.Background(), request); err == nil {
		t.Fatal("metadata test reused a generic consumer's workspace")
	}
	request.OperationID = "metadata-operation"
	if _, err := engine.TestMetadata(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(retained.DatabasePath); err != nil {
		t.Fatal("generic consumer lost its database", err)
	}
	if _, err := engine.TestMetadata(context.Background(), SQLiteRequest{OperationID: "../op1"}); err == nil {
		t.Fatal("invalid operation accepted")
	}
	if _, err := os.Stat(retained.DatabasePath); err != nil {
		t.Fatal("invalid request removed active consumer", err)
	}
}

func TestRestoreMetadataFailuresDisposeOutput(t *testing.T) {
	for _, name := range []string{"verification", "cancellation", "ltx"} {
		t.Run(name, func(t *testing.T) { testRestoreMetadataFailure(t, name) })
	}
}

func testRestoreMetadataFailure(t *testing.T, name string) {
	engine, request, _ := setup(t, fixture(t, fixtureSQL))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	switch name {
	case "verification":
		request.Sentinel.Marker = "wrong"
	case "cancellation":
		engine.Observer = restoreCallbackObserver{next: engine.Observer, before: cancel}
	case "ltx":
		engine.CLI = &fakeCLI{t: t, data: fixture(t, fixtureSQL), txid: "0000000000000008"}
		request.Source = SQLiteSource{Kind: LitestreamLTX, LTX: &LTXSource{BindingID: "b1", Epoch: "e1", TXID: 7, Recoverability: true}}
	}
	receipt, err := engine.TestMetadata(ctx, request)
	if err == nil || receipt.OperationID != "" {
		t.Fatal("failed verification returned success", err)
	}
	assertRestoreWorkspaceEmpty(t, engine.Root)
}

func assertRestoreWorkspaceEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("workspace retains disposable files: %v %v", entries, err)
	}
}

type restoreCallbackObserver struct {
	next   SQLiteSchemaObserver
	before func()
}

func (o restoreCallbackObserver) Observe(ctx context.Context, tx *sql.Tx) (SQLiteSchemaObservation, error) {
	o.before()
	return o.next.Observe(ctx, tx)
}

func TestRestoreMetadataPreservesRunningConsumer(t *testing.T) {
	engine, request, _ := setup(t, fixture(t, fixtureSQL))
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	engine.Observer = restoreCallbackObserver{next: engine.Observer, before: func() {
		close(entered)
		<-release
	}}
	done := make(chan error, 1)
	go func() { _, err := engine.TestMetadata(context.Background(), request); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatal("verification stopped before entering the consumer", err)
	}
	if _, err := engine.TestMetadata(context.Background(), request); err == nil {
		t.Error("concurrent operation reused workspace")
	}
	if _, err := os.Stat(filepath.Join(engine.Root, request.OperationID, "restored.sqlite")); err != nil {
		t.Error("active verification lost output", err)
	}
	once.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertRestoreWorkspaceEmpty(t, engine.Root)
}
