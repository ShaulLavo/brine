//go:build linux

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
)

func TestSecretOperationBeforeRelease(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	intent := ops.Intent{Kind: ops.SecretSet, App: "hello", SecretRef: "hello-token"}
	op, old, err := s.CreateOperation(ctx, intent, "requester", "first")
	if err != nil || old || op.PlanID != "" || op.Kind != ops.SecretSet {
		t.Fatal(op, old, err)
	}
	if err = s.SetOperationState(ctx, op.ID, ops.Preflight); err == nil {
		t.Fatal("secret operation accepted deploy state")
	}
	if err = s.SetOperationState(ctx, op.ID, ops.Preparing); err != nil {
		t.Fatal(err)
	}
	if err = s.SetOperationState(ctx, op.ID, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	same, old, err := s.CreateOperation(ctx, intent, "requester", "first")
	if err != nil || !old || same.ID != op.ID {
		t.Fatal(same, old, err)
	}
	got, err := s.LastOperation(ctx, "hello")
	if err != nil || got.ID != op.ID {
		t.Fatal(got, err)
	}
	if _, err = s.db.Exec("UPDATE operations SET state='starting' WHERE id=?", op.ID); err == nil {
		t.Fatal("SQL accepted illegal secret transition")
	}
}
func TestMigrateV1Operations(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE schema_version(version INTEGER NOT NULL); INSERT INTO schema_version VALUES(1);" + schema); err != nil {
		t.Fatal(err)
	}
	in := fixture(t)
	p, err := plan.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	d := in.Desired
	raw, _ := p.CanonicalBytes()
	desired, _ := d.CanonicalBytes()
	if _, err = db.Exec("INSERT INTO plans VALUES(?,?,?,?,?)", p.Hash, raw, digest(raw), desired, p.DesiredHash); err != nil {
		t.Fatal(err)
	}
	now := timestamp()
	if _, err = db.Exec("INSERT INTO operations VALUES(?,?,?,?,?,?,?)", "old-operation", p.Hash, "requester", "key", ops.Queued, now, now); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(ops.LaunchPayload{Outcome: "intent"})
	if _, err = db.Exec("INSERT INTO events VALUES(?,?,?,?,?,?)", "old-operation", 1, "launch", "", payload, now); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	op, err := s.GetOperation(context.Background(), "old-operation")
	if err != nil || op.Kind != ops.Deploy || op.PlanID != p.Hash {
		t.Fatal(op, err)
	}
	events, err := s.EventsAfter(context.Background(), op.ID, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	if err = s.SetOperationState(context.Background(), op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	var violations int
	rows, err := s.db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		violations++
	}
	rows.Close()
	if violations != 0 {
		t.Fatal("foreign key violations", violations)
	}
}

func TestSecretAuditBoundToOperationIdentity(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.SecretSet, App: "hello", SecretRef: "hello-token"}, "requester", "audit")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"brine.other.hello-token.v1", "brine.hello.other-ref.v1"} {
		payload, _ := json.Marshal(ops.SecretVersionPayload{Name: name, Outcome: "intent"})
		if _, err := s.AppendEvent(ctx, op.ID, ops.Event{Kind: "secret_version", Payload: payload}); err == nil {
			t.Fatal("foreign assignment accepted", name)
		}
	}
	if _, err := s.AppendEvent(ctx, op.ID, ops.Event{Kind: "launch", Payload: json.RawMessage(`{"outcome":"intent"}`)}); err == nil {
		t.Fatal("secret accepted deploy event")
	}
	if _, err := s.db.Exec("UPDATE operations SET secret_ref='other' WHERE id=?", op.ID); err == nil {
		t.Fatal("operation identity mutable")
	}
}
