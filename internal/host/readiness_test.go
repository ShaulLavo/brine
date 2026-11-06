package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/readiness"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestReadinessControlSnapshotNonMutation(t *testing.T) {
	for _, kind := range []string{"missing", "missing_directory", "current", "non_ok", "old", "future", "corrupt"} {
		t.Run(kind, func(t *testing.T) { testReadinessControl(t, kind) })
	}
}
func testReadinessControl(t *testing.T, kind string) {
	t.Helper()
	root := t.TempDir()
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", scratch)
	dir := filepath.Join(root, "state")
	if kind != "missing_directory" {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if kind != "missing" && kind != "missing_directory" {
		readinessStoreFixture(t, dir, kind)
	}
	before := diagnosticFiles(t, dir)
	r := readinessControl{dir: dir}
	value, err := r.Control(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"missing": "missing", "missing_directory": "missing", "current": "current", "non_ok": "current", "old": "schema_upgrade_required", "future": "schema_unsupported", "corrupt": "preview_unavailable"}[kind]
	if value.State != want {
		t.Fatalf("state=%s want=%s", value.State, want)
	}
	if value.QuickCheck.Value != nil && *value.QuickCheck.Value != (kind == "current") {
		t.Fatal(value)
	}
	if kind == "current" || kind == "non_ok" {
		if value.QuickCheck.Value == nil {
			t.Fatal("structural check omitted")
		}
	}
	if kind == "current" {
		backups, err := r.Backups(t.Context())
		if err != nil || backups == nil || len(backups) != 0 {
			t.Fatal(backups, err)
		}
	}
	after := diagnosticFiles(t, dir)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("control DB, sidecars or lock listing changed", before, after)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatal("snapshot scratch leaked", entries, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Control(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation", err)
	}
}
func readinessStoreFixture(t *testing.T, dir, kind string) {
	t.Helper()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	queries := []string{"PRAGMA journal_mode=DELETE"}
	switch kind {
	case "old":
		queries = append(queries, "UPDATE schema_version SET version=1")
	case "future":
		queries = append(queries, "UPDATE schema_version SET version=999")
	case "non_ok":
		queries = append(queries, "CREATE TABLE check_fixture(x INTEGER)", "INSERT INTO check_fixture VALUES(NULL)", "PRAGMA writable_schema=ON", "UPDATE sqlite_master SET sql='CREATE TABLE check_fixture(x INTEGER NOT NULL)' WHERE name='check_fixture'")
	}
	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if kind == "corrupt" {
		if err := os.WriteFile(filepath.Join(dir, "control.db"), []byte("not SQLite planted-secret"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func TestReadinessFactoryHasNoMutationRuntime(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil { // #nosec G302 -- Private fixture directory requires traversal.
		t.Fatal(err)
	}
	opens := 0
	factory := newServerFactory("fixture", "deploy", func(context.Context, string) (*Runtime, error) {
		opens++
		t.Fatal("mutation runtime opened")
		return nil, nil
	}, func(context.Context) (dispatch.Inventory, error) { return &inventory.Collector{}, nil })
	factory.diagnosticStateDir = dir
	server, err := factory.Build(t.Context(), "readiness")
	if err != nil || opens != 0 || server.Readiness == nil || server.Planner != nil || server.Apps != nil {
		t.Fatal(server, err)
	}
	r := server.Readiness.(readiness.Reader)
	r.Inventory = nil
	r.Runtime = nil
	r.Network = nil
	r.Policy = nil
	report, err := r.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	if _, err := readiness.DecodeReport(raw); err != nil {
		t.Fatal(err)
	}
	if report.Ready != readiness.NotReady || len(report.Findings) != 1 || report.Findings[0].Code != "control_store_missing" {
		t.Fatal(report)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatal("missing state initialized")
	}
	if err := factory.Close(); err != nil {
		t.Fatal(err)
	}
}
