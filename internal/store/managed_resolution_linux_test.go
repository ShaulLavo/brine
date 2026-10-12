//go:build linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/target"
)

// Actual immutable control-store joins, not a host deployment/recovery proof.
func managedResolutionFixture(t *testing.T) (*Store, plan.Plan, Operation, Operation) {
	t.Helper()
	ctx := context.Background()
	s, p, source := managedControlFrontendFixture(t, true)
	_, desired, err := s.LoadPlan(ctx, p.Hash)
	if err != nil {
		t.Fatal(err)
	}
	unit, err := quadlet.Render(desired, p, *p.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	intent := ManagedDesiredState{BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, Revision: 1, PlanID: p.Hash, OperationID: source.ID, EngineRunning: true, FrontendRunning: true, IngressServing: true}
	if _, err := s.CommitManagedDesired(ctx, source.ID, 0, intent); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, source.ID, ops.Starting); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindManagedFrontendPermit(ctx, source.ID, p.Hash, unit.Hash()); err != nil {
		t.Fatal(err)
	}
	engine := target.Unit{Name: "brine-engine-" + string(intent.BindingID) + ".service", Hash: "sha256:" + p.Managed.EngineUnitHash}
	frontend := target.Unit{Name: unit.Name(), Hash: unit.Hash()}
	release := Release{ID: source.ID, PlanID: p.Hash, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: []target.Unit{engine, frontend}, CaddyFile: target.CaddyFile{Name: p.App + ".caddy", Hash: "sha256:" + strings.Repeat("a", 64)}, CaddyGeneration: 1, Managed: &ops.ManagedRelease{BindingID: intent.BindingID, EpochID: intent.EpochID, RuntimeSHA256: intent.RuntimeSHA256, EngineUnit: engine, EnginePort: p.Managed.EnginePort, Frontend: &ops.ManagedFrontend{Image: p.Managed.Frontend.Image, Port: p.Managed.Frontend.Port, Unit: frontend, Secrets: p.Secrets}}}
	route := ManagedRouteState{App: p.App, BindingID: intent.BindingID, EpochID: intent.EpochID, RuntimeSHA256: intent.RuntimeSHA256, DesiredRevision: 1, PlanID: p.Hash, OperationID: source.ID, Revision: 1, Generation: 1, Present: true, FileHash: release.CaddyFile.Hash, RootSHA256: strings.Repeat("b", 64), LoadedSHA256: strings.Repeat("c", 64)}
	if _, err := s.CommitManagedRouteState(ctx, 0, route); err != nil {
		t.Fatal(err)
	}
	for _, state := range []ops.State{ops.Checking, ops.Committing} {
		if err := s.SetOperationState(ctx, source.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CommitRelease(ctx, p.App, release); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ops.StepPayload{Step: "managed_commit", Outcome: "completed"})
	if _, err := s.AppendEvent(ctx, source.ID, ops.Event{Kind: "step", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, source.ID, ops.RecoveryRequired); err != nil {
		t.Fatal(err)
	}
	successor, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Resolve, PlanID: p.Hash, RecoveryOf: source.ID}, "requester", "managed-resolution")
	if err != nil {
		t.Fatal(err)
	}
	return s, p, source, successor
}
func finishManagedResolution(t *testing.T, s *Store, id string) {
	t.Helper()
	for _, state := range []ops.State{ops.Preflight, ops.Preparing, ops.Succeeded} {
		if err := s.SetOperationState(context.Background(), id, state); err != nil {
			t.Fatal(err)
		}
	}
}
func TestManagedResolutionSuccessRequiresJoinedProofWithoutChangingSource(t *testing.T) {
	s, p, source, successor := managedResolutionFixture(t)
	ctx := context.Background()
	record, err := s.ReadManagedFrontendRecord(ctx, p.App)
	if err != nil || record.State != ops.RecoveryRequired || record.Resolution.Permit.OperationID != source.ID {
		t.Fatal("read-only record lost unknown source ownership", err)
	}
	if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err == nil {
		t.Fatal("active resolver granted boot")
	}
	finishManagedResolution(t, s, successor.ID)
	resolved, err := s.ReadManagedFrontendPermit(ctx, p.App)
	if err != nil || resolved.Pending || resolved.Permit != record.Resolution.Permit {
		t.Fatal("joined terminal resolution not admitted", err)
	}
	unchanged, err := s.GetOperation(ctx, source.ID)
	if err != nil || unchanged.State != ops.RecoveryRequired {
		t.Fatal("immutable source changed", err)
	}
	if _, err := s.HoldDataFence(ctx, p.Managed.Database.DatabaseID, "concurrent-data-operation"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err == nil {
		t.Fatal("resolved success bypassed current fence")
	}
}
func TestManagedResolutionRejectsFailedAndMixedFamilies(t *testing.T) {
	s, p, source, first := managedResolutionFixture(t)
	ctx := context.Background()
	if err := s.SetOperationState(ctx, first.ID, ops.Failed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err == nil {
		t.Fatal("failed successor granted boot")
	}
	second, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Resolve, PlanID: p.Hash, RecoveryOf: source.ID}, "requester", "managed-resolution-second")
	if err != nil {
		t.Fatal(err)
	}
	finishManagedResolution(t, s, second.ID)
	if _, err := s.ReadManagedFrontendPermit(ctx, p.App); !errors.Is(err, ErrConflict) {
		t.Fatal("mixed failed/successful family granted boot", err)
	}
}

func TestManagedSuccessfulRollbackResolveDoesNotGrantBoot(t *testing.T) {
	s, p, _, successor := managedResolutionFixture(t)
	ctx := context.Background()
	if err := s.SetOperationState(ctx, successor.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, successor.ID, ops.Preparing); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ops.StepPayload{Step: "rollback_check", Outcome: "completed"})
	if _, err := s.AppendEvent(ctx, successor.ID, ops.Event{Kind: "step", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, successor.ID, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err == nil {
		t.Fatal("successful rollback resolver granted a serving frontend")
	}
}

func TestManagedFreshLifecycleDoesNotStretchAnExpiredRecoveredRuntimeProof(t *testing.T) {
	for _, complete := range []bool{false, true} {
		testManagedFreshLifecycleScope(t, complete, false)
		testManagedFreshLifecycleScope(t, complete, true)
	}
}
func testManagedFreshLifecycleScope(t *testing.T, complete, recover bool) {
	t.Helper()
	s, original, source, resolver := managedResolutionFixture(t)
	ctx := context.Background()
	finishManagedResolution(t, s, resolver.ID)
	oldRecord, err := s.ReadManagedFrontendRecord(ctx, original.App)
	if err != nil {
		t.Fatal(err)
	}
	native, err := s.ReadEnginePermit(ctx, original.Managed.Database.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	changed := native.Runtime.Clone()
	changed.Secrets.Publisher.Version++
	fence, err := s.HoldDataFence(ctx, original.Managed.Database.DatabaseID, "current-runtime-change")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitEngineRuntime(ctx, changed, fence.ID, "current-runtime-change"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseDataFence(ctx, fence.ID, "current-runtime-change"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadManagedFrontendPermit(ctx, original.App); err == nil {
		t.Fatal("old resolved proof stretched after native head changed")
	}
	p, desired := managedFreshStartPlan(t, s, original, changed)
	if _, err := s.SavePlan(ctx, p, desired); err != nil {
		t.Fatal(err)
	}
	current, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: p.Hash}, "requester", "fresh-frontend-start")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []ops.State{ops.Preflight, ops.Preparing} {
		if err := s.SetOperationState(ctx, current.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	wanted := ManagedDesiredState{BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, Revision: 2, PlanID: p.Hash, OperationID: current.ID, EngineRunning: true, FrontendRunning: true, IngressServing: true}
	if _, err := s.CommitManagedDesired(ctx, current.ID, 1, wanted); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, current.ID, ops.Starting); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ReadManagedFrontendPermit(ctx, p.App)
	if err != nil || !pending.Pending || pending.Permit.OperationID != current.ID || pending.Permit.UnitHash != oldRecord.Resolution.Permit.UnitHash {
		t.Fatal("fresh owned start not projected", err)
	}
	if err := s.SetOperationState(ctx, current.ID, ops.Checking); err != nil {
		t.Fatal(err)
	}
	route := ManagedRouteState{App: p.App, BindingID: wanted.BindingID, EpochID: wanted.EpochID, RuntimeSHA256: wanted.RuntimeSHA256, DesiredRevision: 2, PlanID: p.Hash, OperationID: current.ID, Revision: 2, Generation: 2, Present: true, FileHash: "sha256:" + strings.Repeat("a", 64), RootSHA256: strings.Repeat("b", 64), LoadedSHA256: strings.Repeat("c", 64)}
	if _, err := s.CommitManagedRouteState(ctx, 1, route); err != nil {
		t.Fatal(err)
	}
	if complete {
		for _, step := range []string{"managed_resume", "managed_frontend_start", "managed_frontend_check", "managed_route", "managed_route_receipt"} {
			appendManagedCompletion(t, s, current.ID, step)
		}
	}
	if recover {
		if err := s.SetOperationState(ctx, current.ID, ops.RecoveryRequired); err != nil {
			t.Fatal(err)
		}
		resolver, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Resolve, PlanID: p.Hash, RecoveryOf: current.ID}, "requester", "fresh-lifecycle-resolve")
		if err != nil {
			t.Fatal(err)
		}
		for _, state := range []ops.State{ops.Preflight, ops.Preparing, ops.Starting, ops.Checking} {
			if err := s.SetOperationState(ctx, resolver.ID, state); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.SetOperationState(ctx, resolver.ID, ops.Succeeded); err != nil {
			t.Fatal(err)
		}
	} else if err := s.SetOperationState(ctx, current.ID, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	granted, permitErr := s.ReadManagedFrontendPermit(ctx, p.App)
	if complete && (permitErr != nil || granted.Pending || granted.Permit.OperationID != current.ID || granted.Permit.RuntimeSHA256 != p.Managed.RuntimeSHA256) {
		t.Fatal("fresh closed serving proof not admitted", permitErr)
	}
	if !complete && permitErr == nil {
		t.Fatal("fresh terminal without start observations admitted")
	}
	if !complete {
		appendManagedCompletion(t, s, current.ID, "managed_frontend_start")
		if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err == nil {
			t.Fatal("post-terminal observation created serving proof")
		}
	}
	rawRecord, err := s.ReadManagedFrontendRecord(ctx, p.App)
	if err != nil || rawRecord.Resolution.Permit != oldRecord.Resolution.Permit {
		t.Fatal("fresh start rewrote original frontend ownership", err)
	}
	if complete {
		testManagedRestoredFreshLifecycle(t, s, original, changed, oldRecord)
	}
	originalOp, err := s.GetOperation(ctx, source.ID)
	if err != nil || originalOp.State != ops.RecoveryRequired {
		t.Fatal("fresh start changed original recovery", err)
	}
}
func managedFreshStartPlan(t *testing.T, s *Store, original plan.Plan, runtime data.EngineRuntime) (plan.Plan, policy.Desired) {
	t.Helper()
	in := managedFreshInput(t, s, original, runtime)
	p, err := plan.BuildLifecycle(in, plan.StartApp)
	if err != nil || p.Kind == plan.Conflict {
		t.Fatal("fresh lifecycle plan", err, p.Conflicts)
	}
	return p, in.Desired
}
func managedFreshInput(t *testing.T, s *Store, original plan.Plan, runtime data.EngineRuntime) plan.Input {
	t.Helper()
	ctx := context.Background()
	_, desired, err := s.LoadPlan(ctx, original.Hash)
	if err != nil {
		t.Fatal(err)
	}
	in := fixture(t)
	in.Desired = desired
	in.Snapshot.Versions.Caddy = target.Known("2.10.2")
	generation, err := s.Generation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	in.Snapshot.Generation = target.Known(generation)
	in.State, err = s.LoadBrineState(ctx, in.Snapshot.Identity, generation)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.CurrentRelease(ctx, original.App)
	if err != nil {
		t.Fatal(err)
	}
	frontend := release.Managed.Frontend
	in.Snapshot.Apps = target.Known([]target.App{{Name: original.App, Image: target.Known(target.Image{Digest: frontend.Image.Digest, Platform: frontend.Image.Platform}), AllocatedHostPort: target.Known(frontend.Port), QuadletUnits: target.Known([]target.Unit{frontend.Unit}), Secrets: target.Known([]target.Secret{})}})
	in.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: release.CaddyGeneration, Files: []target.CaddyFile{release.CaddyFile}})
	native, err := s.ReadEnginePermit(ctx, runtime.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := s.ReadManagedDesired(ctx, runtime.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	route, err := s.ReadManagedRouteState(ctx, original.App)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRaw, _ := json.Marshal(runtime)
	in.Image = frontend.Image
	in.Managed = &plan.ManagedFacts{EngineImage: original.Managed.EngineImage, Database: &native.Reserved.Database, Archive: &native.Reserved.Archive, Runtime: &runtime, Fences: native.Fences, NativeState: target.Known("running"), OwnedNativeState: target.Known("running"), LoggingPrerequisite: target.Known(true), LoggingRootSHA256: strings.Repeat("a", 64), LoggingConfigSHA256: strings.Repeat("b", 64), Intent: &plan.ManagedIntentFacts{Revision: intent.Revision, EpochID: intent.EpochID, RuntimeSHA256: intent.RuntimeSHA256, Current: intent.Current(runtime.BindingID, runtime.EpochID, nativeCanonicalHash(runtimeRaw)), EngineRunning: intent.EngineRunning, FrontendRunning: intent.FrontendRunning, IngressServing: intent.IngressServing}, RouteRevision: route.Revision}
	return in
}

func appendManagedCompletion(t *testing.T, s *Store, operation, step string) {
	t.Helper()
	raw, _ := json.Marshal(ops.StepPayload{Step: step, Outcome: "completed"})
	if _, err := s.AppendEvent(context.Background(), operation, ops.Event{Kind: "step", Payload: raw}); err != nil {
		t.Fatal(err)
	}
}

func testManagedRestoredFreshLifecycle(t *testing.T, s *Store, original plan.Plan, runtime data.EngineRuntime, old ManagedFrontendRecord) {
	t.Helper()
	ctx := context.Background()
	in := managedFreshInput(t, s, original, runtime)
	in.Desired.Environment = append(in.Desired.Environment, policy.Environment{Name: "EXAMPLE", Value: "changed"})
	p, err := plan.Build(in)
	if err != nil || p.Kind != plan.Update {
		t.Fatal("compensation deployment plan", err, p.Conflicts)
	}
	if _, err := s.SavePlan(ctx, p, in.Desired); err != nil {
		t.Fatal(err)
	}
	attempt, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: p.Hash}, "requester", "failed-later-deploy")
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []ops.State{ops.Preflight, ops.Preparing} {
		if err := s.SetOperationState(ctx, attempt.ID, phase); err != nil {
			t.Fatal(err)
		}
	}
	next := ManagedDesiredState{BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, Revision: p.ManagedIntentRevision + 1, OperationID: attempt.ID, PlanID: p.Hash, EngineRunning: true, FrontendRunning: true, IngressServing: true}
	if _, err := s.CommitManagedDesired(ctx, attempt.ID, p.ManagedIntentRevision, next); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, attempt.ID, ops.Starting); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindManagedFrontendPermit(ctx, attempt.ID, p.Hash, "sha256:"+strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, attempt.ID, ops.RollingBack); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreManagedDesired(ctx, attempt.ID, p); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreManagedFrontendPermit(ctx, attempt.ID, old.Resolution.Permit.OperationID); err == nil {
		t.Fatal("missing restoration journal authorized old-scope frontend")
	}
	appendManagedCompletion(t, s, attempt.ID, "managed_restore_desired")
	if err := s.RestoreManagedFrontendPermit(ctx, attempt.ID, old.Resolution.Permit.OperationID); err != nil {
		t.Fatal("fresh prior lifecycle scope not restored", err)
	}
	restored, err := s.ReadManagedFrontendPermit(ctx, p.App)
	if err != nil || restored.Permit.RuntimeSHA256 != p.Managed.RuntimeSHA256 || restored.Permit.OperationID == attempt.ID {
		t.Fatal("failed creation cause masqueraded as serving proof", err)
	}
	if err := s.SetOperationState(ctx, attempt.ID, ops.RolledBack); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err != nil {
		t.Fatal("restored closed scope not retained", err)
	}
	if _, err := s.HoldDataFence(ctx, p.Managed.Database.DatabaseID, "after-rollback"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err == nil {
		t.Fatal("rollback proof bypassed current native fence")
	}
}
