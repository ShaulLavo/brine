//go:build linux

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestV2TerminalRemovalMigrationAndResolution(t *testing.T) {
	ctx := context.Background()
	dir := stateDir(t)
	db, err := sql.Open("sqlite", filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE schema_version(version INTEGER NOT NULL);INSERT INTO schema_version VALUES(1);" + schema); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrateOperations(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE plan_inputs(id TEXT PRIMARY KEY,canonical BLOB NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: db, dir: dir}
	r := release(t, legacy, "initial-release")
	r.Units = r.Units[:1]
	if err = legacy.CommitRelease(ctx, "hello", r); err != nil {
		t.Fatal(err)
	}
	in := fixture(t)
	_, in.Desired, err = legacy.LoadPlan(ctx, r.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	in.Snapshot.Generation = target.Known(uint64(1))
	in.State, err = legacy.LoadBrineState(ctx, in.Snapshot.Identity, 1)
	if err != nil {
		t.Fatal(err)
	}
	in.Snapshot.Apps = target.Known([]target.App{{Name: "hello", Image: target.Known(target.Image{Digest: r.Image.Digest, Platform: r.Image.Platform}), AllocatedHostPort: target.Known(r.HostPort), QuadletUnits: target.Known(r.Units), Secrets: target.Known([]target.Secret{})}})
	in.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: r.CaddyGeneration, Files: []target.CaddyFile{r.CaddyFile}})
	p, err := plan.BuildRemove(in)
	if err != nil || p.Kind != plan.Update {
		t.Fatal(p, err)
	}
	if _, err = legacy.SavePlan(ctx, p, in.Desired); err != nil {
		t.Fatal(err)
	}
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	stamp := timestamp()
	if _, err = db.Exec("INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?,?)", id, p.Hash, "fixture", "source", ops.RecoveryRequired, stamp, stamp, ops.Deploy, p.App, ""); err != nil {
		t.Fatal(err)
	}
	steps := []ops.StepPayload{{Step: "preflight", Outcome: "completed"}, {Step: "withdraw_route", Outcome: "completed"}, {Step: "stop_unit", Outcome: "unknown", Code: "interrupted"}}
	for i, step := range steps {
		raw, _ := json.Marshal(step)
		if _, err = db.Exec("INSERT INTO events VALUES(?,?,?,?,?,?)", id, i+1, "step", "", raw, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("DROP TABLE plan_inputs"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(dir, "control.db"), 0600); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	source, err := migrated.GetOperation(ctx, id)
	if err != nil || source.State != ops.RecoveryRequired || source.RecoveryOf != "" {
		t.Fatal(source, err)
	}
	before, err := migrated.EventsAfter(ctx, id, 0, 128)
	if err != nil || len(before) != 3 {
		t.Fatal(before, err)
	}
	intent := ops.Intent{Kind: ops.Resolve, PlanID: p.Hash, RecoveryOf: id}
	successor, existing, err := migrated.CreateOperation(ctx, intent, "fixture", "resolution-key")
	if err != nil || existing {
		t.Fatal(successor, err)
	}
	adopted, err := migrated.EventsAfter(ctx, successor.ID, 0, 128)
	if err != nil || len(adopted) != 4 || adopted[0].Kind != "resolution" {
		t.Fatal(adopted, err)
	}
	for i, event := range before {
		if string(event.Payload) != string(adopted[i+1].Payload) {
			t.Fatal("lost boundary evidence")
		}
	}
	replay, existing, err := migrated.CreateOperation(ctx, intent, "fixture", "resolution-key")
	if err != nil || !existing || replay.ID != successor.ID {
		t.Fatal(replay, err)
	}
	if _, _, err = migrated.CreateOperation(ctx, intent, "fixture", "other-key"); err == nil {
		t.Fatal("parallel resolution accepted")
	}
	for _, state := range []ops.State{ops.Preflight, ops.Preparing, ops.Quiescing, ops.Starting, ops.Checking, ops.Committing} {
		if err = migrated.SetOperationState(ctx, successor.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	if err = migrated.RetireApp(ctx, successor.ID, p.App, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = migrated.SetOperationState(ctx, successor.ID, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	unchanged, err := migrated.GetOperation(ctx, id)
	if err != nil || !reflect.DeepEqual(source, unchanged) {
		t.Fatal(unchanged, err)
	}
	after, err := migrated.EventsAfter(ctx, id, 0, 128)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal(after, err)
	}
	for _, query := range []string{"UPDATE operations SET recovery_of='other' WHERE id=?", "UPDATE operations SET state='preparing' WHERE id=?"} {
		if _, err = migrated.db.Exec(query, successor.ID); err == nil {
			t.Fatal("lost identity or terminal guard")
		}
	}
}

func TestResolutionFencesActiveAndSuccessfulDescendantsAcrossAncestry(t *testing.T) {
	for _, settled := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "succeeded"}[settled], func(t *testing.T) {
			s, err := Open(stateDir(t))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			root, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.SecretSet, App: "hello", SecretRef: "token"}, "fixture", "root")
			if err != nil {
				t.Fatal(err)
			}
			for _, state := range []ops.State{ops.Preparing, ops.RecoveryRequired} {
				if err = s.SetOperationState(ctx, root.ID, state); err != nil {
					t.Fatal(err)
				}
			}
			makeChild := func(source, key string) Operation {
				op, _, e := s.CreateOperation(ctx, ops.Intent{Kind: ops.Resolve, App: "hello", SecretRef: "token", RecoveryOf: source}, "fixture", key)
				if e != nil {
					t.Fatal(e)
				}
				return op
			}
			middle := makeChild(root.ID, "middle")
			for _, state := range []ops.State{ops.Preflight, ops.RecoveryRequired} {
				if err = s.SetOperationState(ctx, middle.ID, state); err != nil {
					t.Fatal(err)
				}
			}
			descendant := makeChild(middle.ID, "descendant")
			if settled {
				for _, state := range []ops.State{ops.Preflight, ops.Succeeded} {
					if err = s.SetOperationState(ctx, descendant.ID, state); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, _, err = s.CreateOperation(ctx, ops.Intent{Kind: ops.Resolve, App: "hello", SecretRef: "token", RecoveryOf: root.ID}, "fixture", "old-source-again"); !errors.Is(err, ErrConflict) {
				t.Fatalf("ancestor accepted despite descendant: %v", err)
			}
		})
	}
}
