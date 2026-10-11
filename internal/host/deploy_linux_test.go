//go:build linux

package host

import (
	"fmt"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/spec"
)

func TestDeploymentStateDistinguishesUnallocatedAndPreparedWithoutMutation(t *testing.T) {
	r, _, _, _ := connectedPreparationRig(t)
	ctx := t.Context()
	app, err := spec.Parse([]byte(r.spec))
	if err != nil {
		t.Fatal(err)
	}
	state, err := r.service.DeploymentState(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Databases) != 1 || state.Databases[0].Prepared || state.Databases[0].Initialized || state.Databases[0].Credentials {
		t.Fatal("unallocated reported ready", state)
	}
	firstHash := state.ReviewHash
	preparation := r.call(t, "data_prepare_plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: preparation.PlanID, IdempotencyKey: "state-fixture"})
	if err = r.runner.Run(ctx, accepted.Data.(jobs.Accepted).OperationID); err != nil {
		t.Fatal(err)
	}
	state, err = r.service.DeploymentState(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	if state.ReviewHash != firstHash || !state.Databases[0].Prepared || state.Databases[0].Initialized || state.Databases[0].Credentials {
		t.Fatal("wrong prepared state", state)
	}
}

func TestInitializationCandidateRetainsEveryPreparedDatabase(t *testing.T) {
	r, _, _, _ := connectedPreparationRig(t)
	original, err := spec.Parse([]byte(r.spec))
	if err != nil {
		t.Fatal(err)
	}
	r.spec += fmt.Sprintf(`
[[databases]]
name="audit"
persistent_root=%q
mount_path="/audit"
filename="audit.db"
backup_destination="primary"
[[schema_compatibility]]
database="audit"
accepts=[%q]
startup="preserve"
`, original.Databases[0].PersistentRoot, data.EmptyMarker)
	preparation := r.call(t, "data_prepare_plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: preparation.PlanID, IdempotencyKey: "several-databases"}).Data.(jobs.Accepted)
	if err = r.runner.Run(t.Context(), accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	candidate, err := r.store.InitializationCandidate(t.Context(), string(original.Name), preparation.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.Bindings) != 2 || candidate.Bindings[0].Name != "audit" || candidate.Bindings[1].Name != "main" || len(candidate.Allocations) != 2 {
		t.Fatal("candidate dropped declared data", candidate.Bindings)
	}
}

func TestDeploymentOperationLookupIsRequesterBoundAndReadOnly(t *testing.T) {
	r, _, _, _ := connectedPreparationRig(t)
	planned := r.call(t, "data_prepare_plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	q := dispatch.DeploymentOperationQuery{PlanID: planned.PlanID, Kind: ops.Deploy, Key: "lookup-fixture"}
	before := r.call(t, "deploy_operation", q).Data.(dispatch.DeploymentOperation)
	if before.OperationID != "" {
		t.Fatal("invented acceptance", before)
	}
	accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: q.Key}).Data.(jobs.Accepted)
	recovered := r.call(t, "deploy_operation", q).Data.(dispatch.DeploymentOperation)
	if recovered.OperationID != accepted.OperationID {
		t.Fatal("lost acceptance", recovered, accepted)
	}
	other := r.service
	other.Requester = "another-requester"
	hidden, err := other.DeploymentOperation(t.Context(), q)
	if err != nil || hidden.OperationID != "" {
		t.Fatal("cross-requester lookup", hidden, err)
	}
	for _, kind := range []ops.Kind{ops.CredentialActivation, ops.DataInitApply} {
		intent := ops.Intent{Kind: kind, App: "hello", SecretRef: planned.PlanID}
		job, _, err := r.store.CreateOperation(t.Context(), intent, r.service.Requester, string(kind)+"-lookup")
		if err != nil {
			t.Fatal(err)
		}
		value, err := r.service.DeploymentOperation(t.Context(), dispatch.DeploymentOperationQuery{Kind: kind, PlanID: planned.PlanID})
		if err != nil || value.OperationID != job.ID {
			t.Fatal(value, err)
		}
		if _, _, err = r.store.CreateOperation(t.Context(), intent, r.service.Requester, string(kind)+"-second"); err != nil {
			t.Fatal(err)
		}
		if _, err = r.service.DeploymentOperation(t.Context(), dispatch.DeploymentOperationQuery{Kind: kind, PlanID: planned.PlanID}); err == nil {
			t.Fatal("ambiguous tasks admitted")
		}

	}
}

func TestDeploymentCheckpointRejectsAnotherAppsValidRetainedPlan(t *testing.T) {
	r, _, _, _ := connectedPreparationRig(t)
	app, err := spec.Parse([]byte(r.spec))
	if err != nil {
		t.Fatal(err)
	}
	own, err := r.service.PlanDataPreparation(t.Context(), app)
	if err != nil {
		t.Fatal(err)
	}
	q := dispatch.DeploymentCheckpointArgs{Spec: r.spec, PlanID: own.PlanID, Step: "preparation"}
	checked := r.call(t, "deploy_checkpoint", q).Data.(dispatch.DeploymentCheckpoint)
	if checked.Planned == nil || checked.Planned.PlanID != own.PlanID {
		t.Fatal("did not retrieve actual retained review", checked)
	}
	alien := app
	alien.Name = "another-app"
	foreign, err := r.service.PlanDataPreparation(t.Context(), alien)
	if err != nil {
		t.Fatal(err)
	}
	if foreign.Kind == "conflict" {
		t.Fatal("foreign plan must be valid for the regression", foreign)
	}
	q.PlanID = foreign.PlanID
	if _, err = r.service.DeploymentCheckpoint(t.Context(), app, q); err == nil {
		t.Fatal("another app's retained preparation accepted")
	}
	q.PlanID = own.PlanID
	q.Step = "release"
	if _, err = r.service.DeploymentCheckpoint(t.Context(), app, q); err == nil {
		t.Fatal("preparation substituted for release")
	}
	changed := app
	changed.ContainerPort++
	q.Step = "preparation"
	if _, err = r.service.DeploymentCheckpoint(t.Context(), changed, q); err == nil {
		t.Fatal("same app's different desired input accepted")
	}
}
