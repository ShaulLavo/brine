package host

import (
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
)

func (r *deployRig) deploySecretRelease(t *testing.T, key string) ops.Release {
	t.Helper()
	planned := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	if planned.Kind != plan.Create && planned.Kind != plan.Update {
		t.Fatalf("deploy plan %+v", planned)
	}
	return r.applySecretPlan(t, planned.PlanID, key)
}

func (r *deployRig) applySecretPlan(t *testing.T, planID, key string) ops.Release {
	t.Helper()
	accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: planID, IdempotencyKey: key}).Data.(jobs.Accepted)
	if err := r.runner.Run(t.Context(), accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	release, err := r.store.CurrentRelease(t.Context(), "hello")
	if err != nil || release.PlanID != planID {
		t.Fatal(release, err)
	}
	return release
}

func TestRollbackAppliesRecordedSecretVersionAfterRotation(t *testing.T) {
	v1 := target.Secret{Name: "brine.hello.hello-token.v1", ID: "fixture-token-001"}
	v2 := target.Secret{Name: "brine.hello.hello-token.v2", ID: "fixture-token-002"}
	r := newDeployRig(t)
	r.spec += "[secrets]\nTOKEN = \"hello-token\"\n"
	r.inventory.secrets = []target.Secret{v1}
	r.inventory.snapshot.Apps = target.Known([]target.App{{Name: "hello", Image: target.Observation[target.Image]{Status: target.Absent}, AllocatedHostPort: target.Observation[target.Port]{Status: target.Absent}, QuadletUnits: target.Known([]target.Unit{}), Secrets: target.Known([]target.Secret{v1}), UDPPublication: target.Observation[target.UDPPublication]{Status: target.Absent}}})
	first := r.deploySecretRelease(t, "fixture-first")
	if len(first.Secrets) != 1 || first.Secrets[0].VersionName != v1.Name {
		t.Fatalf("first release %+v", first.Secrets)
	}
	r.inventory.secrets = []target.Secret{v1, v2}
	rotated := r.deploySecretRelease(t, "fixture-rotated")
	if len(rotated.Secrets) != 1 || rotated.Secrets[0].VersionName != v2.Name {
		t.Fatalf("forward deploy must bind the newest version: %+v", rotated.Secrets)
	}

	envelope, err := r.tryCall(t, "rollback", dispatch.RollbackArgs{App: "hello"})
	if err != nil {
		t.Fatalf("rollback to %s (recorded %s) refused with %s: %v", first.ID, first.Secrets[0].VersionName, result.Classify(err).Code(), err)
	}
	planned := envelope.Data.(apps.RollbackPlan)
	stored, _, err := r.store.LoadPlan(t.Context(), planned.PlanID)
	if err != nil || planned.ReleaseID != first.ID || planned.Kind != plan.Update || len(stored.Secrets) != 1 || stored.Secrets[0] != first.Secrets[0] {
		t.Fatalf("rollback plan %+v binds %+v, want %+v (%v)", planned, stored.Secrets, first.Secrets, err)
	}
	restored := r.applySecretPlan(t, planned.PlanID, "fixture-rollback")
	if len(restored.Secrets) != 1 || restored.Secrets[0] != first.Secrets[0] {
		t.Fatalf("applied rollback binds %+v, want %+v", restored.Secrets, first.Secrets)
	}

	explicit := r.call(t, "rollback", dispatch.RollbackArgs{App: "hello", ReleaseID: rotated.ID}).Data.(apps.RollbackPlan)
	stored, _, err = r.store.LoadPlan(t.Context(), explicit.PlanID)
	if err != nil || explicit.Kind != plan.Update || len(stored.Secrets) != 1 || stored.Secrets[0] != rotated.Secrets[0] {
		t.Fatalf("explicit rollback binds %+v, want %+v (%v)", stored.Secrets, rotated.Secrets, err)
	}

	r.inventory.secrets = []target.Secret{v1}
	missing := r.call(t, "rollback", dispatch.RollbackArgs{App: "hello", ReleaseID: rotated.ID}).Data.(apps.RollbackPlan)
	stored, _, err = r.store.LoadPlan(t.Context(), missing.PlanID)
	if err != nil || missing.Kind != plan.Conflict || len(missing.Conflicts) != 1 || missing.Conflicts[0] != (plan.Diagnostic{Code: plan.SecretMissing, Field: "secrets.TOKEN"}) || len(stored.Secrets) != 0 || len(stored.Changes) != 0 {
		t.Fatalf("pruned recorded version planned %+v binds %+v (%v)", missing, stored.Secrets, err)
	}
	accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: missing.PlanID, IdempotencyKey: "fixture-missing"}).Data.(jobs.Accepted)
	if err := r.runner.Run(t.Context(), accepted.OperationID); err == nil {
		t.Fatal("conflict plan executed")
	}
	head, err := r.store.CurrentRelease(t.Context(), "hello")
	if err != nil || head.ID != restored.ID {
		t.Fatal(head.ID, err)
	}
}
