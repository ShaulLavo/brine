package data

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Compare observable catalogs and query results, not sqlite_schema.sql's
// whitespace: SQLite retains formatting there, which normalization removes.
func TestSchemaNormalizationPreservesSemantics(t *testing.T) {
	raw, err := os.ReadFile("testdata/schema-goldens.json")
	if err != nil {
		t.Fatal(err)
	}
	var goldens []struct{ Name, SQL string }
	if err := json.Unmarshal(raw, &goldens); err != nil {
		t.Fatal(err)
	}
	cases := make([]struct{ name, schema, seed string }, 0, len(goldens)+8)
	for _, g := range goldens {
		cases = append(cases, struct{ name, schema, seed string }{g.Name, g.SQL, "INSERT INTO t VALUES('value')"})
	}
	for _, selectSQL := range []string{
		"x 'AB'", "X 'AB'", "x/**/'AB'", "X/* gap */'AB'", "x'AB' AS blob", "X'AB' AS blob",
		"'a' || 'b' AS joined", "'a'/* gap */||/* gap */'b' AS joined",
		"'a'||'b'", "'a'/* gap */||'b'", "x+1", "x + 1", "x'AB'", "X'AB'", "(x)", "x/* trailing */",
		"\"x\" AS \"quoted alias\"", "[x] AS [quoted alias]", "`x` AS `quoted alias`",
		"'semi; -- /* ''quoted''' AS literal", "1.2e-3 + .5 AS number",
	} {
		cases = append(cases, struct{ name, schema, seed string }{selectSQL, "CREATE TABLE t(x TEXT);CREATE VIEW v AS SELECT " + selectSQL + " FROM t;", "INSERT INTO t VALUES('value')"})
	}
	cases = append(cases, struct{ name, schema, seed string }{
		"forward-view-dependencies", "CREATE VIEW v AS SELECT * FROM inner_view;CREATE VIEW inner_view AS SELECT 'a'||'b' FROM t;CREATE TABLE t(x TEXT);", "INSERT INTO t VALUES('value')",
	})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			initializer, err := CompileSchemaFile(t.Context(), "main", "", []byte(c.schema))
			if err != nil {
				t.Fatal(err)
			}
			normalized := strings.Join(initializer.Statements, ";")
			before := schemaSemantics(t, c.schema, c.seed)
			after := schemaSemantics(t, normalized, c.seed)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("normalization changed semantics\noriginal: %#v\nnormalized: %#v\nSQL: %s", before, after, normalized)
			}
		})
	}
}

func schemaSemantics(t *testing.T, schema, seed string) []any {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(t.Context(), schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	catalog := schemaQuery(t, db, "SELECT type,name,tbl_name FROM sqlite_schema ORDER BY type,name")
	result := []any{catalog}
	for _, row := range catalog[1:] {
		fields := row.([]any)
		kind, name := fields[0].(string), fields[1].(string)
		quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		if kind == "index" {
			result = append(result, schemaQuery(t, db, "PRAGMA index_xinfo("+quoted+")"))
		} else {
			result = append(result, schemaQuery(t, db, "PRAGMA table_xinfo("+quoted+")"), schemaQuery(t, db, "SELECT * FROM "+quoted))
		}
	}
	return result
}

func schemaQuery(t *testing.T, db *sql.DB, query string) []any {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	result := []any{columns}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for i, value := range values {
			if b, ok := value.([]byte); ok {
				values[i] = fmt.Sprintf("blob:%x", b)
			}
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestInitializePreservesInferredViewNames(t *testing.T) {
	raw := "CREATE VIEW v AS SELECT 'a'/* gap */||'b';"
	initializer, err := CompileSchemaFile(t.Context(), "main", "", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	binding, filename := schemaFixture(t)
	allocation, err := CaptureAllocation(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := InitializeSchema(t.Context(), binding, initializer, &allocation); err != nil {
		t.Fatal(err)
	}
	observed := ObserveSQLiteSchema(t.Context(), binding, []SQLiteSchemaDefinition{initializer.Definition})
	if observed.State != VerifiedSchema || observed.CatalogSHA256 != initializer.Definition.CatalogSHA256 {
		t.Fatalf("host changed catalog: %+v", observed)
	}
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	got := schemaQuery(t, db, "SELECT * FROM v")
	want := []any{[]string{"'a'/* gap */||'b'"}, []any{"ab"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("host changed view names or values: %#v", got)
	}
}
