//go:build linux

package host

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestRestoreFactoryDoesNotOpenMutationRuntime(t *testing.T) {
	factory := newServerFactory("fixture", "deploy", func(context.Context, string) (*Runtime, error) {
		t.Fatal("restore opened mutation runtime")
		return nil, nil
	}, func(context.Context) (dispatch.Inventory, error) {
		t.Fatal("restore inspected live host inventory")
		return nil, nil
	})
	server, err := factory.Build(context.Background(), "restore_test")
	if err != nil || server.RestoreTests == nil {
		t.Fatal("restore reader not wired", err)
	}
	if err := factory.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestRestoreDeclarationsUseOnlySelectedCommittedDatabase(t *testing.T) {
	scope := store.CredentialScope{Database: data.DatabaseBinding{Name: "main", Root: "/never-open-live"}, Replica: data.ReplicaBinding{BindingID: data.ReplicaBindingID(strings.Repeat("a", 32)), EpochID: data.ReplicaEpochID(strings.Repeat("b", 32)), Committed: true}}
	declarations := store.WriterSchema{Desired: policy.Desired{SchemaCompatibility: []data.SchemaCompatibility{{Database: "main", Accepts: []string{"v1", data.EmptyMarker}}}, RestoreInvariants: []data.RestoreInvariant{{Database: "main", Kind: "row_count", Table: "orders", Count: 7}, {Database: "audit", Kind: "non_null", Table: "events", Column: "message"}}}, Definitions: []data.SchemaDefinition{{Database: "main", Marker: "v1", CatalogSHA256: strings.Repeat("c", 64)}}}
	request, err := restoreRequest(scope, declarations, dispatch.RestoreTestArgs{App: "hello", Database: "main"})
	if err != nil || !request.Latest || !request.Source.LTX.Recoverability || len(request.AcceptedSchemas) != 2 || len(request.Invariants) != 1 || request.Invariants[0].Table != "orders" {
		t.Fatal("committed selected declarations lost", err)
	}
	if request.Source.LTX.Barrier != nil {
		t.Fatal("read-only test invented upload evidence")
	}
}
func TestFixedRestoreObserverUsesCallerIsolatedTransaction(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	observer := restoreSchemaObserver{database: "main"}
	schema, err := observer.Observe(context.Background(), tx)
	if err != nil || schema.State != restore.VerifiedEmpty || schema.CatalogSHA256 != data.EmptyCatalogSHA256 {
		t.Fatal("fixed isolated observer failed", err)
	}
}
