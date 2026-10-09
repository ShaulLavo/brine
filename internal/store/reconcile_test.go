//go:build linux

package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
)

func TestV1MigrationPreservesDeployJournalAndForeignKeys(t *testing.T) {
	ctx := context.Background()
	dir := stateDir(t)
	db, err := sql.Open("sqlite", filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE schema_version(version INTEGER NOT NULL); INSERT INTO schema_version VALUES(1);" + schema); err != nil {
		t.Fatal(err)
	}
	for from, tos := range ops.Transitions() {
		for _, to := range tos {
			if _, err = db.Exec("INSERT INTO transitions VALUES(?,?)", from, to); err != nil {
				t.Fatal(err)
			}
		}
	}
	legacy := &Store{db: db, dir: dir}
	id := operation(t, legacy)
	payload, _ := json.Marshal(ops.LaunchPayload{Outcome: "intent"})
	if _, err := legacy.AppendEvent(ctx, id, ops.Event{Kind: "launch", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := legacy.TransitionOperation(ctx, id, ops.Queued, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	before, err := legacy.GetOperation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	events, err := legacy.EventsAfter(ctx, id, 0, 128)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "control.db")
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = OpenPreviewReadOnly(ctx, dir)
	var older *SchemaError
	if !errors.As(err, &older) || older.Version != 1 {
		t.Fatalf("v1 preview %v", err)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	infoAfter, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, unchanged) || !info.ModTime().Equal(infoAfter.ModTime()) {
		t.Fatal("v1 preview changed bytes/mtime")
	}
	migrated, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	after, err := migrated.GetOperation(ctx, id)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("operation changed: %#v %#v %v", before, after, err)
	}
	got, err := migrated.EventsAfter(ctx, id, 0, 128)
	if err != nil || !reflect.DeepEqual(events, got) {
		t.Fatalf("journal changed: %#v %#v %v", events, got, err)
	}
	if _, _, err := migrated.LoadPlan(ctx, before.PlanID); err != nil {
		t.Fatal(err)
	}
	recovery, err := migrated.CreateReconcileOperation(ctx, "fixture-requester")
	if err != nil || recovery.Kind != "reconcile" || recovery.PlanID != "" {
		t.Fatalf("recovery intent: %#v %v", recovery, err)
	}
	if err := migrated.TransitionOperation(ctx, recovery.ID, ops.Queued, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	if err := migrated.TransitionOperation(ctx, recovery.ID, ops.Preflight, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	if err := migrated.TransitionOperation(ctx, recovery.ID, ops.Succeeded, ops.Preflight); err == nil {
		t.Fatal("terminal receipt changed")
	}
	var version, foreignKeys int
	if err := migrated.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("schema %d %v", version, err)
	}
	if err := migrated.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatal("foreign keys not restored")
	}
	if _, err := migrated.db.Exec("UPDATE operations SET plan_id='missing' WHERE id=?", id); err == nil {
		t.Fatal("migration lost plan FK")
	}
	if _, err := migrated.db.Exec("UPDATE operations SET state='starting' WHERE id=?", id); err == nil {
		t.Fatal("migration lost state trigger")
	}
	if _, err := migrated.db.Exec("DELETE FROM events WHERE operation_id=?", id); err == nil {
		t.Fatal("migration lost append-only trigger")
	}
}
