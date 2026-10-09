//go:build linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
)

func TestDiagnosticReads(t *testing.T) {
	s := openTest(t)
	id := operation(t, s)
	ctx := context.Background()
	payload, _ := json.Marshal(ops.FailurePayload{Code: "stale_plan"})
	if _, e := s.AppendEvent(ctx, id, ops.Event{Kind: "failure", Payload: payload}); e != nil {
		t.Fatal(e)
	}
	if e := s.SetOperationState(ctx, id, ops.Failed); e != nil {
		t.Fatal(e)
	}
	ro, e := OpenReadOnly(ctx, s.dir)
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	names, e := ro.AppNames(ctx)
	if e != nil || len(names) != 1 || names[0] != "hello" {
		t.Fatalf("%v %v", names, e)
	}
	records, e := ro.RecentOperations(ctx, "hello", 10)
	if e != nil || len(records) != 1 || records[0].Operation.ID != id || records[0].FailureCode != "stale_plan" || records[0].Operation.State != ops.Failed {
		t.Fatalf("%+v %v", records, e)
	}
	if _, e := ro.RecentOperations(ctx, "different", 10); e != nil {
		t.Fatal(e)
	}
	if _, e := ro.RecentOperations(ctx, "hello", 11); !errors.Is(e, ErrInvalid) {
		t.Fatalf("unbounded query %v", e)
	}
	if _, e := ro.db.ExecContext(ctx, "UPDATE schema_version SET version=2"); e == nil {
		t.Fatal("read-only connection accepted SQL mutation")
	}
	info, e := os.Stat(filepath.Join(s.dir, "control.db"))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode changed %v %v", info, e)
	}
}
func TestReadOnlyDoesNotInitializeState(t *testing.T) {
	dir := stateDir(t)
	if _, e := OpenReadOnly(context.Background(), dir); e == nil {
		t.Fatal("opened absent database")
	}
	files, e := os.ReadDir(dir)
	if e != nil || len(files) != 0 {
		t.Fatalf("created state %v %v", files, e)
	}
}
func TestReadOnlyRefusesSymlinksAndOpenPermissions(t *testing.T) {
	s := openTest(t)
	dir := stateDir(t)
	if e := os.Symlink(filepath.Join(s.dir, "control.db"), filepath.Join(dir, "control.db")); e != nil {
		t.Fatal(e)
	}
	if _, e := OpenReadOnly(context.Background(), dir); e == nil {
		t.Fatal("followed symlink")
	}
	if e := os.Chmod(s.dir, 0755); e != nil {
		t.Fatal(e)
	}
	if _, e := OpenReadOnly(context.Background(), s.dir); e == nil {
		t.Fatal("opened public state")
	}
	info, e := os.Stat(s.dir)
	if e != nil || info.Mode().Perm() != 0755 {
		t.Fatal("read-only open changed permissions")
	}
}

func TestReadGenerationDoesNotInitializeEmptyStore(t *testing.T) {
	dir := t.TempDir()
	generation, err := ReadGeneration(context.Background(), dir)
	if err != nil || generation != 0 {
		t.Fatalf("generation %d %v", generation, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("read-only inventory created control files")
	}
	if err := os.WriteFile(filepath.Join(dir, "unknown"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadGeneration(context.Background(), dir); err == nil {
		t.Fatal("unknown state became generation zero")
	}
}
