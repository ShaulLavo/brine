package store

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func makeExactV36RemovalFixture(t *testing.T, s *Store) {
	t.Helper()
	var exists, count int
	if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name='data_sqlite_archive_attempts'").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists == 0 {
		return
	}
	err := s.db.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM data_sqlite_archive_attempts)+(SELECT count(*) FROM data_sqlite_retire_inputs)+(SELECT count(*) FROM operations WHERE kind IN ('sqlite_retire','sqlite_retire_resolve'))`).Scan(&count)
	if err != nil || count != 0 {
		t.Fatal("cannot rewind active removal state", count, err)
	}
	execRemovalFixture(t, s, "PRAGMA foreign_keys=OFF; PRAGMA legacy_alter_table=ON; DROP TRIGGER data_fences_sqlite_archive_release")
	for _, table := range []string{"data_sqlite_archive_recovery", "data_sqlite_archive_completions", "data_sqlite_archive_claims", "data_sqlite_archive_attempts", "data_sqlite_retire_inputs", "data_sqlite_retire_settlements"} {
		execRemovalFixture(t, s, "DROP TABLE "+table)
	}
	execRemovalFixture(t, s, "DROP TRIGGER data_sqlite_point_consumers_task_exclusive; DROP TRIGGER data_sqlite_consumer_completions_task_exclusive")
	for _, name := range []string{"data_sqlite_point_consumers_identity", "data_sqlite_consumer_completions_exact"} {
		execRemovalFixture(t, s, "DROP TRIGGER "+name+"; "+frozenRemovalSQL(t, "migration_v35.go", "CREATE TRIGGER "+name, "END;")+"END;")
	}
	restoreSQLiteConsumerUniqueFixture(t, s, "data_sqlite_point_consumers")
	restoreSQLiteConsumerUniqueFixture(t, s, "data_sqlite_consumer_completions")
	operations := frozenRemovalSQL(t, "migration_v36.go", "CREATE TABLE operations_v36", "`+dropsSchema")
	execRemovalFixture(t, s, operations)
	execRemovalFixture(t, s, "DELETE FROM transitions WHERE kind IN ('sqlite_retire','sqlite_retire_resolve'); UPDATE schema_version SET version=36; PRAGMA legacy_alter_table=OFF; PRAGMA foreign_keys=ON")
}

func frozenRemovalSQL(t *testing.T, file, start, end string) string {
	t.Helper()
	var raw []byte
	var err error
	switch file {
	case "migration_v35.go":
		raw, err = os.ReadFile("migration_v35.go")
	case "migration_v36.go":
		raw, err = os.ReadFile("migration_v36.go")
	default:
		t.Fatal("not a frozen migration fixture")
	}
	if err != nil {
		t.Fatal(err)
	}
	_, tail, ok := strings.Cut(string(raw), start)
	if !ok {
		t.Fatal("missing SQL start", file, start)
	}
	body, _, ok := strings.Cut(tail, end)
	if !ok {
		t.Fatal("missing SQL end", file, end)
	}
	return start + body
}

func execRemovalFixture(t *testing.T, s *Store, statement string) {
	t.Helper()
	if _, err := s.db.ExecContext(t.Context(), statement); err != nil {
		t.Fatal(statement, err)
	}
}

func restoreSQLiteConsumerUniqueFixture(t *testing.T, s *Store, table string) {
	t.Helper()
	var ddl string
	if err := s.db.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(t.Context(), "SELECT sql FROM sqlite_master WHERE type='trigger' AND tbl_name=? ORDER BY name", table)
	if err != nil {
		t.Fatal(err)
	}
	var triggers []string
	for rows.Next() {
		var statement string
		if err = rows.Scan(&statement); err != nil {
			t.Fatal(err)
		}
		triggers = append(triggers, statement)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	ddl = strings.Replace(ddl, table, table+"_fixture", 1)
	ddl = strings.Replace(ddl, "operation_id TEXT NOT NULL", "operation_id TEXT NOT NULL UNIQUE", 1)
	execRemovalFixture(t, s, ddl+"; INSERT INTO "+table+"_fixture SELECT * FROM "+table+"; DROP TABLE "+table+"; ALTER TABLE "+table+"_fixture RENAME TO "+table+";")
	for _, statement := range triggers {
		execRemovalFixture(t, s, statement)
	}
}

func makeExactV35DropFixture(t *testing.T, s *Store) {
	t.Helper()
	makeExactV36RemovalFixture(t, s)
	requireNoDropMetadataForRewind(t, s)
	// Rebuild the operations CHECK from the frozen migration itself, not by
	// relabeling the candidate schema. Every U5 object is removed from this fixture.
	source, err := os.ReadFile("migration_v35.go")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(source), "`")
	if !ok {
		t.Fatal("missing v35 SQL")
	}
	ddl, _, ok := strings.Cut(rest, "CREATE TABLE data_sqlite_task_inputs")
	if !ok {
		t.Fatal("missing v35 table boundary")
	}
	statements := []string{`PRAGMA foreign_keys=OFF;
 DROP TABLE drop_gateway_configs; DROP TABLE drop_materialized_usage; DROP TABLE drop_reaps; DROP TABLE drop_disposals; DROP TABLE drop_effects; DROP TABLE drop_seals; DROP TABLE drop_chunks; DROP TABLE drop_upload_files; DROP TABLE drop_gateway_heads; DROP TABLE drop_grants; DROP TABLE drop_reservations;
 DROP TABLE drop_names; DROP TABLE drop_incarnations; DROP TABLE drop_uploads;
 DROP TABLE drop_attempts; DROP TABLE drop_plans;
 DELETE FROM transitions WHERE kind IN ('drop_upload_prepare','drop_apply','drop_resolve');
 PRAGMA legacy_alter_table=ON;`, ddl, `PRAGMA legacy_alter_table=OFF;
 UPDATE schema_version SET version=35; PRAGMA foreign_keys=ON;`}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
}

func requireNoDropMetadataForRewind(t *testing.T, s *Store) {
	t.Helper()
	var count int
	err := s.db.QueryRowContext(t.Context(), `SELECT
 (SELECT count(*) FROM drop_gateway_configs)+(SELECT count(*) FROM drop_gateway_heads)+(SELECT count(*) FROM drop_grants)+
 (SELECT count(*) FROM drop_reservations)+(SELECT count(*) FROM drop_names)+
 (SELECT count(*) FROM drop_incarnations)+(SELECT count(*) FROM drop_uploads)+
 (SELECT count(*) FROM drop_attempts)+(SELECT count(*) FROM drop_plans)+
 (SELECT count(*) FROM operations WHERE kind IN ('drop_upload_prepare','drop_apply','drop_resolve'))`).Scan(&count)
	if err != nil || count != 0 {
		t.Fatal("cannot rewind a fixture containing drop state", count, err)
	}
}

func dropRowsFingerprint(t *testing.T, s *Store, queries []string) string {
	t.Helper()
	var all []byte
	for _, query := range queries {
		all = append(all, dropQueryBytes(t, s, query)...)
	}
	return digest(all)
}

func dropQueryBytes(t *testing.T, s *Store, query string) []byte {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result []byte
	for rows.Next() {
		result = append(result, dropRowBytes(t, rows, len(cols))...)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func dropRowBytes(t *testing.T, rows interface{ Scan(...any) error }, count int) []byte {
	t.Helper()
	values := make([]any, count)
	refs := make([]any, count)
	for i := range refs {
		refs[i] = &values[i]
	}
	if err := rows.Scan(refs...); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
