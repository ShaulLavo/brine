package data

import (
	"context"
	"database/sql"
	"testing"
)

func TestTransactionObserverKeepsCallerTransactionOpen(t *testing.T) {
	_, path := schemaFixture(t)
	db := createSchema(t, path, "CREATE TABLE t(x TEXT)", MarkerTableSQL)
	_, fingerprint, err := CatalogFingerprint([]CatalogRow{{"table", "t", "t", str("CREATE TABLE t(x TEXT)")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO brine_schema_marker VALUES(1,?,?)", "v1", fingerprint); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	definitions := []SchemaDefinition{{Database: "main", Marker: "v1", CatalogSHA256: fingerprint}}
	observation := ObserveSchemaTransaction(context.Background(), tx, "main", definitions)
	if observation.State != VerifiedSchema || observation.CatalogSHA256 != fingerprint {
		t.Fatalf("fixed observer lost schema: %+v", observation)
	}
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM t").Scan(&count); err != nil {
		t.Fatal("observer closed caller transaction", err)
	}
	if wrong := ObserveSchemaTransaction(context.Background(), tx, "other", definitions); wrong.State != Unknown {
		t.Fatal("foreign database definition accepted")
	}
	if missing := ObserveSchemaTransaction(context.Background(), nil, "main", definitions); missing.State != Unknown {
		t.Fatal("nil transaction accepted")
	}
}
