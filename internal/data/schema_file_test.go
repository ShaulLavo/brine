package data

import (
	"context"
	"strings"
	"testing"
)

func TestSchemaFileCanonicalMarker(t *testing.T) {
	ctx := context.Background()
	a, err := CompileSchemaFile(ctx, "main", "", []byte("CREATE TABLE t(x TEXT);"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := CompileSchemaFile(ctx, "main", "", []byte("-- comment\nCREATE   TABLE t ( x /* comment */ TEXT ) ;\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Definition != b.Definition || a.Definition.Marker != "sha256:"+a.Definition.CatalogSHA256[:12] {
		t.Fatalf("unstable definition: %+v %+v", a, b)
	}
	changed, err := CompileSchemaFile(ctx, "main", "", []byte("CREATE TABLE t(x INTEGER);"))
	if err != nil || changed.Definition.Marker == a.Definition.Marker {
		t.Fatalf("change not detected: %v", err)
	}
	if err := a.Validate(ctx); err != nil {
		t.Fatalf("host rejects client artifact: %v", err)
	}
}

func TestSchemaFileRefusesAtLine(t *testing.T) {
	for _, statement := range []string{"INSERT INTO t VALUES(1);", "CREATE TRIGGER tr AFTER INSERT ON t BEGIN SELECT 1; END;", "DROP TABLE t;", "CREATE TABLE t(x) CREATE TABLE u(y);", "CREATE TABLE t(x)", "CREATE TABLE t(x); DELETE FROM t;"} {
		_, err := CompileSchemaFile(context.Background(), "main", "", []byte("-- header\n\n"+statement))
		if err == nil || !strings.Contains(err.Error(), "line 3:") {
			t.Fatalf("missing line refusal for %q: %v", statement, err)
		}
	}
}

func TestSchemaFilePreservesSQLiteTokens(t *testing.T) {
	raw := `CREATE TABLE café("quoted name" TEXT DEFAULT 'semi; -- /*', n REAL DEFAULT 1.2e-3, b BLOB DEFAULT X'AB', count INTEGER DEFAULT 1_000);
CREATE INDEX i ON café("quoted name");
CREATE VIEW v AS SELECT "quoted name", n + .5, b FROM café;`
	a, err := CompileSchemaFile(t.Context(), "main", "", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, refused := range []string{"CREATE TABLE t AS SELECT 1;", "CREATE TABLE brine_schema_marker(x);", "CREATE TABLE temp.t(x);", "CREATE TABLE t(x);\nCREATE TRIGGER tr AFTER INSERT ON t BEGIN SELECT 1; END;"} {
		if _, err := CompileSchemaFile(t.Context(), "main", "", []byte(refused)); err == nil {
			t.Fatal("accepted unsupported schema")
		}
	}
	a.Statements = []string{"CREATE TABLE t(x); CREATE TABLE u(y)"}
	if err := a.Validate(t.Context()); err == nil {
		t.Fatal("host accepted multiple statements in one entry")
	}
}

func TestSchemaFileRefusesInvalidForeignKeys(t *testing.T) {
	if _, err := CompileSchemaFile(t.Context(), "main", "", []byte("CREATE TABLE parent(x TEXT);\nCREATE TABLE child(x TEXT REFERENCES parent(x));")); err == nil {
		t.Fatal("client accepted invalid foreign key schema")
	}
}
