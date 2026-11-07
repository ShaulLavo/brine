//go:build linux

package host

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
)

func removalControlDB(t *testing.T, root string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func removalRowCount(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestSQLiteRetireApplyRefusesCorruptLookupWithoutDeployFallback(t *testing.T) {
	r := connectedRemovalVariant(t, "empty")
	p := r.plan(t)
	db := removalControlDB(t, r.source.prep.root)
	before := removalRowCount(t, db, "SELECT count(*) FROM operations")
	if _, err := db.ExecContext(t.Context(), "DROP TRIGGER data_archive_plans_no_UPDATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "UPDATE data_archive_plans SET canonical=? WHERE id=?", []byte(`{}`), p.PlanID); err != nil {
		t.Fatal(err)
	}
	response, err := r.response(t, "apply", dispatch.ApplyArgs{PlanID: p.PlanID, IdempotencyKey: "corrupt-lookup"})
	if err == nil || response.OK {
		t.Fatal("corrupt plan accepted")
	}
	if removalRowCount(t, db, "SELECT count(*) FROM operations") != before {
		t.Fatal("lookup fell through to deploy")
	}
}
func TestSQLiteRetireApplyRefusesExpiredAndHostLocalPlans(t *testing.T) {
	for _, kind := range []string{"expired", "host-local"} {
		testRemovalUnroutablePlan(t, kind)
	}
}
func testRemovalUnroutablePlan(t *testing.T, kind string) {
	source := newUnclaimedEmptyRemovalHostRig(t, false)
	workflow := source.service
	h := workflow.Host.(archiveHost)
	if kind == "expired" {
		d, _ := source.plan.Candidate.Databases[0].SQLite()
		evidence := d.Retention[0]
		evidence.VerifiedAt = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
		h.service.Policy = &fakePolicy{p: removalPolicy(t, d.Binding.Root, evidence)}
		workflow.Now = func() time.Time { return time.Now().UTC().Add(-16 * time.Minute) }
	}
	if kind == "host-local" {
		h.removal = false
	}
	workflow.Host = h
	p, err := workflow.Plan(t.Context(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	r := connectRemovalRig(t, source)
	db := removalControlDB(t, source.prep.root)
	before := removalRowCount(t, db, "SELECT count(*) FROM operations")
	response, err := r.response(t, "apply", dispatch.ApplyArgs{PlanID: p.ID, IdempotencyKey: kind})
	expected := result.PlanStale
	if kind == "host-local" {
		expected = result.PolicyRefused
	}
	if err == nil || response.OK || result.Classify(err).Code() != expected {
		t.Fatal(response, err)
	}
	if removalRowCount(t, db, "SELECT count(*) FROM operations") != before {
		t.Fatal("unroutable plan created an operation")
	}
}
func TestSQLiteRetireMissingPreparationSavesNothing(t *testing.T) {
	u := newUnreleasedSQLiteRig(t)
	u.initialize(t)
	r := connectUnreleasedRemoval(t, u)
	db := removalControlDB(t, u.physical.root)
	before := removalRowCount(t, db, "SELECT count(*) FROM data_archive_plans")
	response, err := r.response(t, "lifecycle", dispatch.LifecycleArgs{App: "hello", Action: plan.RemoveApp})
	if err == nil || response.OK || result.Classify(err).Code() != result.RemovalPreparationRequired {
		t.Fatal(response, err)
	}
	if removalRowCount(t, db, "SELECT count(*) FROM data_archive_plans") != before || removalRowCount(t, db, "SELECT count(*) FROM data_archive_operations") != 0 {
		t.Fatal("refusal saved removal work")
	}
}
func TestSQLiteRetireNativeAllocationRefusesBeforeFactsAndEffects(t *testing.T) {
	f := freshNativeMaterialFixture(t)
	raw, err := os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	inv := &removalInventory{state: f.service.Store, snapshot: snapshot}
	f.service.Inventory = inv
	workflow := newArchiveService(f.service, f.stateRoot, f.home, localexec.Session{}, nil, nil, nil)
	app := apps.Service{Store: f.service.Store, Inventory: inv, LoadPolicy: f.service.Policy.Load}
	runtime := &Runtime{Authorize: func(context.Context, dispatch.Class) error { return nil }, Runner: jobs.Runner{TaskHandlers: map[ops.Kind]jobs.TaskHandler{}}, close: func() error { return nil }}
	wireSQLiteRemoval(runtime, app, workflow, jobs.Service{Store: f.service.Store, Requester: f.service.Requester, Launcher: &fakeLaunch{}}, f.service.Store)
	factory := newServerFactory("fixture", "deploy", func(context.Context, string) (*Runtime, error) { return runtime, nil }, nil)
	server, err := factory.Build(t.Context(), "lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	request, err := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: 1, Op: "lifecycle", RequestID: "fixture", Args: json.RawMessage(fmt.Sprintf(`{"app":%q,"action":"remove_app"}`, f.plan.App))})
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Handle(t.Context(), bytes.NewReader(request))
	if err == nil || response.OK || result.Classify(err).Code() != result.PersistentArchiveRequired {
		t.Fatal(response, err)
	}
	db := removalControlDB(t, f.stateRoot)
	if removalRowCount(t, db, "SELECT count(*) FROM data_archive_plans") != 0 {
		t.Fatal("native allocation became SQLite archive")
	}
}

func TestSQLiteRetireStatusRefusesCorruptTerminalJoin(t *testing.T) {
	r := connectRemovalRig(t, newUnclaimedRemovalHostRigSource(t, false, false))
	p := r.plan(t)
	accepted := r.apply(t, p)
	if err := r.runtime.Runner.Run(t.Context(), accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	if r.status(t, accepted.OperationID).Operation.State != ops.Succeeded {
		t.Fatal("no original success")
	}
	db := removalControlDB(t, r.source.prep.root)
	if _, err := db.ExecContext(t.Context(), "DROP TRIGGER data_sqlite_archive_completions_no_UPDATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "UPDATE data_sqlite_archive_completions SET outcome='abandoned' WHERE archive_id=?", p.Archive.ArchiveID); err != nil {
		t.Fatal(err)
	}
	response, err := r.response(t, "operation", dispatch.OperationArgs{OperationID: accepted.OperationID})
	if err == nil || response.OK {
		t.Fatal("metadata status trusted crossed protected completion")
	}
}
