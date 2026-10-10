package data

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCatalogGoldenVectors(t *testing.T) {
	for _, tc := range []struct {
		rows        []CatalogRow
		bytes, hash string
	}{
		{nil, `[]`, EmptyCatalogSHA256},
		{[]CatalogRow{{"table", "t", "t", str("CREATE TABLE t(x TEXT)")}}, `[["table","t","t","CREATE TABLE t(x TEXT)"]]`, "688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"},
		{[]CatalogRow{{"table", "t", "t", str("CREATE TABLE t(x TEXT CHECK(x < 'z'))")}}, `[["table","t","t","CREATE TABLE t(x TEXT CHECK(x \u003c 'z'))"]]`, "ba63ad9fe787183ed1f090284ea94e14eded8dd5cdaa8803ba4d66d79abdc559"},
	} {
		b, h, err := CatalogFingerprint(tc.rows)
		if err != nil || string(b) != tc.bytes || h != tc.hash {
			t.Fatalf("golden %q %s %v", b, h, err)
		}
	}
	rows := []CatalogRow{{"view", "z", "z", nil}, {"table", "é", "é", str("<&  ")}}
	b, _, err := CatalogFingerprint(rows)
	if err != nil || string(b) != `[["table","é","é","\u003c\u0026\u2028\u2029"],["view","z","z",null]]` {
		t.Fatalf("escaping/order %s %v", b, err)
	}
	if rows[0].Type != "view" {
		t.Fatal("catalog sort mutated borrowed input")
	}
}
func str(s string) *string { return &s }
func schemaFixture(t *testing.T) (DatabaseBinding, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	inc := AppIncarnationID("11111111111111111111111111111111")
	id := DatabaseID("22222222222222222222222222222222")
	relative, err := RelativeDirectory(inc, id)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, relative)
	if err = os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	b := DatabaseBinding{DatabaseID: id, IncarnationID: inc, Name: "main", Root: PersistentRoot(root), RelativeDirectory: relative, Filename: "app.db", MountPath: "/data", ReplicaBindingID: "33333333333333333333333333333333"}
	return b, filepath.Join(source, "app.db")
}
func createSchema(t *testing.T, p string, statements ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, s := range statements {
		if _, err = db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	return db
}
func TestObserverEmptyMissingAndUnknown(t *testing.T) {
	b, p := schemaFixture(t)
	ctx := context.Background()
	o := ObserveSchema(ctx, b, nil)
	if o.State != Unknown || o.UnknownReason != "missing" {
		t.Fatalf("missing inferred empty: %+v", o)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("observer created missing DB")
	}
	db := createSchema(t, p, "PRAGMA user_version=0")
	db.Close()
	o = ObserveSchema(ctx, b, nil)
	if o.State != VerifiedEmpty || o.Marker != EmptyMarker || o.CatalogSHA256 != EmptyCatalogSHA256 {
		t.Fatalf("empty observation: %+v", o)
	}
	if err := os.WriteFile(p, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if o = ObserveSchema(ctx, b, nil); o.State != Unknown {
		t.Fatal("zero-byte inferred empty")
	}
}
func TestObserverVerifiedMarkerAndInternalTables(t *testing.T) {
	b, p := schemaFixture(t)
	db := createSchema(t, p, "CREATE TABLE t(x TEXT)", MarkerTableSQL)
	_, hash, err := CatalogFingerprint([]CatalogRow{{"table", "t", "t", str("CREATE TABLE t(x TEXT)")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO brine_schema_marker VALUES(1,?,?)", "v1", hash); err != nil {
		t.Fatal(err)
	}
	definition := []SchemaDefinition{{Database: "main", Marker: "v1", CatalogSHA256: hash}}
	if o := ObserveSchema(context.Background(), b, definition); o.State != VerifiedSchema || o.Marker != "v1" {
		t.Fatalf("marker: %+v", o)
	}
	if _, err = db.Exec("CREATE TABLE _litestream_seq (id INTEGER PRIMARY KEY, seq INTEGER); CREATE TABLE _litestream_lock (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if o := ObserveSchema(context.Background(), b, definition); o.State != VerifiedSchema {
		t.Fatalf("pinned internal tables: %+v", o)
	}
	if _, err = db.Exec("DROP TABLE _litestream_lock; CREATE TABLE _litestream_lock (id INTEGER, injected TEXT)"); err != nil {
		t.Fatal(err)
	}
	if o := ObserveSchema(context.Background(), b, definition); o.State != Unknown {
		t.Fatalf("malformed internal table excluded: %+v", o)
	}
}
func TestObserverRefusesSymlinkOrBroadPermissions(t *testing.T) {
	b, p := schemaFixture(t)
	db := createSchema(t, p, "PRAGMA user_version=0")
	db.Close()
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if o := ObserveSchema(context.Background(), b, nil); o.State != Unknown {
		t.Fatal("broad file mode accepted")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "other.db")
	other := createSchema(t, target, "PRAGMA user_version=0")
	other.Close()
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	if o := ObserveSchema(context.Background(), b, nil); o.State != Unknown {
		t.Fatal("symlink accepted")
	}
}

func TestObserverLiveWALUsesCommittedCatalog(t *testing.T) {
	b, p := schemaFixture(t)
	db := createSchema(t, p, "PRAGMA journal_mode=WAL", "CREATE TABLE t(x TEXT)", MarkerTableSQL)
	db.SetMaxOpenConns(1)
	_, hash, err := CatalogFingerprint([]CatalogRow{{"table", "t", "t", str("CREATE TABLE t(x TEXT)")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO brine_schema_marker VALUES(1,?,?)", "v1", hash); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = os.Chmod(p+suffix, 0600); err != nil {
			t.Fatal(err)
		}
	}
	definitions := []SchemaDefinition{{Database: "main", Marker: "v1", CatalogSHA256: hash}}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("CREATE TABLE not_committed(y INTEGER)"); err != nil {
		t.Fatal(err)
	}
	o := ObserveSchema(context.Background(), b, definitions)
	if o.State != VerifiedSchema || o.CatalogSHA256 != hash {
		t.Fatalf("read uncommitted catalog or skipped WAL: %+v", o)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if o = ObserveSchema(context.Background(), b, definitions); o.State != Unknown || o.UnknownReason != "marker_mismatch" {
		t.Fatalf("fresh committed schema change missed: %+v", o)
	}
}

func TestObserverNewSiblingsMustRemainPrivate(t *testing.T) {
	b, p := schemaFixture(t)
	db := createSchema(t, p, "PRAGMA user_version=0")
	db.Close()
	if err := os.WriteFile(p+"-shm", nil, 0644); err != nil {
		t.Fatal(err)
	}
	if o := ObserveSchema(context.Background(), b, nil); o.State != Unknown || o.UnknownReason != "unverified_sibling" {
		t.Fatalf("unsafe sibling admitted: %+v", o)
	}
	info, err := os.Stat(p + "-shm")
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("observer repaired sibling")
	}
}

func TestFreshWriterCompatibilityRefusesChangedSchema(t *testing.T) {
	b, p := schemaFixture(t)
	db := createSchema(t, p, "PRAGMA user_version=0")
	compatibility := []SchemaCompatibility{{Database: "main", Startup: "preserve", Accepts: []string{EmptyMarker}}}
	if !WriterCompatible(context.Background(), []DatabaseBinding{b}, compatibility, nil) {
		t.Fatal("fresh empty schema refused")
	}
	if _, err := db.Exec("CREATE TABLE t(x INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if WriterCompatible(context.Background(), []DatabaseBinding{b}, compatibility, nil) {
		t.Fatal("cached empty compatibility accepted changed schema")
	}
	if WriterCompatible(context.Background(), []DatabaseBinding{b}, nil, nil) {
		t.Fatal("missing compatibility accepted")
	}
}

func TestWriterCompatibilityRequiresExactOneToOneSet(t *testing.T) {
	b, p := schemaFixture(t)
	createSchema(t, p, "PRAGMA user_version=0")
	c := SchemaCompatibility{Database: "main", Startup: "preserve", Accepts: []string{EmptyMarker}}
	other := c
	other.Database = "other"
	for _, tc := range []struct {
		name          string
		bindings      []DatabaseBinding
		compatibility []SchemaCompatibility
	}{
		{"unmatched compatibility", []DatabaseBinding{b}, []SchemaCompatibility{c, other}},
		{"duplicate binding", []DatabaseBinding{b, b}, []SchemaCompatibility{c}},
		{"duplicate and unmatched", []DatabaseBinding{b, b}, []SchemaCompatibility{c, other}},
		{"duplicate compatibility", []DatabaseBinding{b}, []SchemaCompatibility{c, c}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if WriterCompatible(context.Background(), tc.bindings, tc.compatibility, nil) {
				t.Fatal("non-bijective writer declarations accepted")
			}
		})
	}
}
