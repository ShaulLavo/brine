//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/store"
)

func connectedPreparationRig(t *testing.T) (*deployRig, DataPreparation, *preparationPullRunner, reconcile.Reconciler) {
	t.Helper()
	r := newDeployRig(t)
	_, desired, facts, preparation, _, pull := allocationFixture(t)
	facts.Store = r.store
	preparation.State = r.store
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), "Registry.Example.com:5000", "ghcr.io"))
	raw = []byte(strings.ReplaceAll(string(raw), `persistent_roots = ["/srv/brine/data"]`, fmt.Sprintf("persistent_roots = [%q]", desired.Databases[0].PersistentRoot)))
	raw = append(raw, []byte(`
[[backup_destinations]]
reference="primary"
endpoint="https://storage.example"
region="region-1"
bucket="backups"
base_prefix="brine"
credential_ref="primary"
`)...)
	pol, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	r.policy.p = pol
	r.spec += fmt.Sprintf(`
[runtime]
uid=10001
gid=10001
[[databases]]
name="main"
persistent_root=%q
mount_path="/data"
filename="app.db"
backup_destination="primary"
[[schema_compatibility]]
database="main"
accepts=[%q]
startup="preserve"
`, desired.Databases[0].PersistentRoot, data.EmptyMarker)
	r.service.Data = facts
	r.server.Planner = r.service
	engine := apply.Executor{Journal: r.store, Plans: r.store, Releases: releases{r.store}, PersistentData: preparation}
	inspector := r.runner.Reconciler.(runnerReconciler).reconciler.Systemd
	recovery := newReconciler(r.service, engine, inspector)
	r.runner.Executor = Executor{Service: r.service, Engine: engine}
	r.runner.Reconciler = runnerReconciler{recovery}
	r.runner.Recovery = recoveryJob(recovery)
	return r, preparation, pull, recovery
}

func TestConnectedPreparationPlansWithoutAllocationThenAppliesExactIDs(t *testing.T) {
	r, _, pull, _ := connectedPreparationRig(t)
	ctx := context.Background()
	preparation := r.call(t, "data_prepare_plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	if preparation.Kind != plan.Create {
		t.Fatalf("preparation plan: %+v", preparation)
	}
	p, d, err := r.store.LoadPlan(ctx, preparation.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	second := r.call(t, "data_prepare_plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	p2, _, err := r.store.LoadPlan(ctx, second.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if p.DataAllocations[0].Database.DatabaseID == p2.DataAllocations[0].Database.DatabaseID {
		t.Fatal("independent proposals unexpectedly shared IDs")
	}
	ordinary := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	if ordinary.Kind != plan.Conflict {
		t.Fatal("unallocated proposal permitted deploy")
	}
	if _, err = r.store.ActiveDataIncarnation(ctx, "hello"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("planning reserved identities", err)
	}
	entries, err := os.ReadDir(string(d.Databases[0].PersistentRoot))
	if err != nil || len(entries) != 0 || pull.calls != 0 {
		t.Fatalf("planning changed root/runtime: %v %v calls=%d", entries, err, pull.calls)
	}
	accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: preparation.PlanID, IdempotencyKey: "prepare-fixture"}).Data.(jobs.Accepted)
	if err = r.runner.Run(ctx, accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	status := r.call(t, "operation", dispatch.OperationArgs{OperationID: accepted.OperationID}).Data.(jobs.Status)
	if status.Operation.State != ops.Succeeded || pull.calls != 1 || r.units.installs != 0 || r.health.calls != 0 || r.pulls != 0 {
		t.Fatalf("preparation executed deployment: %+v pulls=%d", status, pull.calls)
	}
	scopes, err := r.store.ReadCredentialScopes(ctx, "hello")
	if err != nil || len(scopes) != 1 || scopes[0].Database != p.DataAllocations[0].Database || scopes[0].Replica != p.DataAllocations[0].Replica {
		t.Fatalf("scope changed: %+v %v", scopes, err)
	}
	if _, err = r.store.CurrentRelease(ctx, "hello"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("preparation committed release", err)
	}
	renamed := strings.Replace(r.spec, `name="main"`, `name="renamed"`, 1)
	renamed = strings.Replace(renamed, `database="main"`, `database="renamed"`, 1)
	// A rejected alternate declaration may neither append a reservation nor poison
	// the original immutable candidate writer database set.
	request, _ := json.Marshal(dispatch.PlanArgs{Spec: renamed})
	wire, _ := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: 1, Op: "data_prepare_plan", RequestID: "rename", Args: request})
	if _, err = r.server.Handle(ctx, strings.NewReader(string(wire))); err == nil {
		t.Fatal("renamed database preparation accepted")
	}
	if _, err = r.store.CandidateWriterSchema(ctx, p.DataAllocations[0].Database.IncarnationID, d); err != nil {
		t.Fatal("unapproved rename poisoned allocated incarnation", err)
	}
}

func TestPreparationRecoveryInspectsCompletionWithoutReplayingEffects(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			r, preparation, pull, recovery := connectedPreparationRig(t)
			ctx := context.Background()
			planned := r.call(t, "data_prepare_plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
			p, d, err := r.store.LoadPlan(ctx, planned.PlanID)
			if err != nil {
				t.Fatal(err)
			}
			operation, _, err := r.store.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: p.Hash}, r.service.Requester, "interrupted-preparation")
			if err != nil {
				t.Fatal(err)
			}
			if err = r.store.SetOperationState(ctx, operation.ID, ops.Preflight); err != nil {
				t.Fatal(err)
			}
			if err = r.store.SetOperationState(ctx, operation.ID, ops.Preparing); err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(ops.StepPayload{Step: "prepare_data", Outcome: "intent"})
			if _, err = r.store.AppendEvent(ctx, operation.ID, ops.Event{Kind: "step", Payload: payload}); err != nil {
				t.Fatal(err)
			}
			if complete {
				if err = preparation.PreparePersistent(ctx, operation.ID, p, d); err != nil {
					t.Fatal(err)
				}
			}
			before := pull.calls
			report, err := recovery.Reconcile(ctx)
			if err != nil || len(report.Outcomes) != 1 {
				t.Fatalf("recovery: %+v %v", report, err)
			}
			want := ops.RecoveryRequired
			if complete {
				want = ops.Succeeded
			}
			final, err := r.store.GetOperation(ctx, operation.ID)
			if err != nil || final.State != want || pull.calls != before {
				t.Fatalf("replayed/incorrect recovery: %+v %v calls=%d/%d", final, err, pull.calls, before)
			}
		})
	}
}
