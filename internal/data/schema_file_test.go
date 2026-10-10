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
		if err == nil || !strings.Contains(err.Error(), "line ") {
			t.Fatalf("missing line refusal for %q: %v", statement, err)
		}
	}
}
