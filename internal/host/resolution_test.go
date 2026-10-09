package host

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

func seedTerminalRemoval(t *testing.T, h *removalHost) string {
	t.Helper()
	ctx := context.Background()
	planned := h.call(t, "lifecycle", dispatch.LifecycleArgs{App: "hello", Action: plan.RemoveApp}).Data.(apps.ConfigPlan)
	op, _, err := h.store.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: planned.PlanID}, "fixture", "stuck-remove")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []ops.State{ops.Preflight, ops.Preparing, ops.Quiescing} {
		if err = h.store.SetOperationState(ctx, op.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	disk, err := h.read()
	if err != nil {
		t.Fatal(err)
	}
	disk.Snapshot.CaddyConfig.Value.Generation++
	disk.Snapshot.CaddyConfig.Value.Files = nil
	disk.Snapshot.LiveCaddyFiles.Value = &[]target.LiveCaddyFile{}
	disk.Active = false
	disk.RouteReloaded = true
	disk.Snapshot.UsedPorts.Value = &[]target.Port{}
	disk.Snapshot.PortOwners.Value = &[]target.PortOwner{}
	if err = h.write(disk, ""); err != nil {
		t.Fatal(err)
	}
	for _, step := range []ops.StepPayload{{Step: "preflight", Outcome: "completed"}, {Step: "withdraw_route", Outcome: "completed"}, {Step: "stop_unit", Outcome: "unknown", Code: "interrupted"}} {
		raw, _ := json.Marshal(step)
		if _, err = h.store.AppendEvent(ctx, op.ID, ops.Event{Kind: "step", Payload: raw}); err != nil {
			t.Fatal(err)
		}
	}
	if err = h.store.SetOperationState(ctx, op.ID, ops.RecoveryRequired); err != nil {
		t.Fatal(err)
	}
	return op.ID
}

func TestResolveTerminalPiRemovalThroughDispatcherAndRunner(t *testing.T) {
	h := newRemovalHost(t, t.TempDir(), true)
	source := seedTerminalRemoval(t, h)
	ctx := context.Background()
	report, err := h.server.Reconciler.DryRun(ctx)
	if err != nil || len(report.Outcomes) != 0 {
		t.Fatal(report, err)
	}
	accepted := h.call(t, "resolve", dispatch.ResolveArgs{OperationID: source, IdempotencyKey: "resolve-key"}).Data.(jobs.Accepted)
	if err = h.runner.Run(ctx, accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	sourceOp, err := h.store.GetOperation(ctx, source)
	if err != nil || sourceOp.State != ops.RecoveryRequired {
		t.Fatal(sourceOp, err)
	}
	status := h.call(t, "operation", dispatch.OperationArgs{OperationID: accepted.OperationID}).Data.(jobs.Status)
	if status.Operation.Kind != ops.Resolve || status.Operation.RecoveryOf != source || status.Operation.State != ops.Succeeded {
		t.Fatal(status)
	}
	disk, err := h.read()
	if err != nil || disk.Active || len(*disk.Snapshot.Apps.Value) != 0 || len(disk.Snapshot.CaddyConfig.Value.Files) != 0 {
		t.Fatal(disk, err)
	}
	_, err = h.store.CurrentRelease(ctx, "hello")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestResolveUnknownSecretAssignmentNeverRecreatesValue(t *testing.T) {
	for _, throughReconcile := range []bool{false, true} {
		for _, exists := range []bool{false, true} {
			t.Run(map[bool]string{false: "absent", true: "present"}[exists], func(t *testing.T) {
				h := newRemovalHost(t, t.TempDir(), true)
				ctx := context.Background()
				source, _, err := h.store.CreateOperation(ctx, ops.Intent{Kind: ops.SecretSet, App: "hello", SecretRef: "hello-token"}, "fixture", "secret-source")
				if err != nil {
					t.Fatal(err)
				}
				if err = h.store.SetOperationState(ctx, source.ID, ops.Preparing); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(ops.SecretVersionPayload{Name: "brine.hello.hello-token.v1", Outcome: "unknown"})
				if _, err = h.store.AppendEvent(ctx, source.ID, ops.Event{Kind: "secret_version", Payload: raw}); err != nil {
					t.Fatal(err)
				}
				if err = h.store.SetOperationState(ctx, source.ID, ops.RecoveryRequired); err != nil {
					t.Fatal(err)
				}
				fake := h.runner.Executor.(Executor).Engine.Podman.(*podman.Fake)
				fake.CreateSecretFunc = func(context.Context, podman.Name, []byte) error { t.Fatal("replayed secret creation"); return nil }
				fake.SecretExistsFunc = func(_ context.Context, name podman.Name) (bool, error) {
					if name.String() != "brine.hello.hello-token.v1" {
						t.Fatal(name)
					}
					return exists, nil
				}
				accepted := h.call(t, "resolve", dispatch.ResolveArgs{OperationID: source.ID, IdempotencyKey: "secret-resolve"}).Data.(jobs.Accepted)
				if throughReconcile {
					if err = h.store.SetOperationState(ctx, accepted.OperationID, ops.Preflight); err != nil {
						t.Fatal(err)
					}
					preview, e := h.server.Reconciler.DryRun(ctx)
					if e != nil || len(preview.Outcomes) != 1 {
						t.Fatal(preview, e)
					}
					if _, err = h.server.Reconciler.Reconcile(ctx); err != nil {
						t.Fatal(err)
					}
				} else if err = h.runner.Run(ctx, accepted.OperationID); err != nil {
					t.Fatal(err)
				}
				op, err := h.store.GetOperation(ctx, accepted.OperationID)
				wanted := ops.Failed
				if exists {
					wanted = ops.Succeeded
				}
				if err != nil || op.State != wanted {
					t.Fatal(op, err)
				}
				original, err := h.store.GetOperation(ctx, source.ID)
				if err != nil || original.State != ops.RecoveryRequired {
					t.Fatal(original, err)
				}
			})
		}
	}
}

type recordedRemovalUnits struct {
	removalUnits
	recorded string
}

func (u recordedRemovalUnits) VerifyRemove(ctx context.Context, name, hash string) error {
	if hash != u.recorded {
		return fmt.Errorf("removal did not use recorded artifact hash")
	}
	return u.removalUnits.VerifyRemove(ctx, name, hash)
}
func (u recordedRemovalUnits) Remove(ctx context.Context, name, hash string) error {
	if hash != u.recorded {
		return fmt.Errorf("removal did not use recorded artifact hash")
	}
	return u.removalUnits.Remove(ctx, name, hash)
}

func TestResolveTerminalRemovalUsesRecordedPreLogDriverHash(t *testing.T) {
	h := newRemovalHost(t, t.TempDir(), true)
	ctx := context.Background()
	release, err := h.store.CurrentRelease(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	p, d, err := h.store.LoadPlan(ctx, release.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the recorded pre-log-policy artifact. Historical ownership is a
	// content hash, not permission to reinterpret bytes through a new renderer.
	var old strings.Builder
	for _, line := range strings.Split(string(current.Bytes()), "\n") {
		if strings.HasPrefix(line, "LogDriver=") || strings.HasPrefix(line, "LogOpt=") {
			continue
		}
		old.WriteString(line)
		old.WriteString("\n")
	}
	previousBytes := strings.TrimSuffix(old.String(), "\n")
	previousHash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(previousBytes)))
	if previousHash == current.Hash() {
		t.Fatal("fixture did not retain historical renderer bytes")
	}
	release.ID = "historical-release"
	for i := range release.Units {
		if release.Units[i].Name == current.Name() {
			release.Units[i].Hash = previousHash
		}
	}
	if err = h.store.CommitRelease(ctx, "hello", release); err != nil {
		t.Fatal(err)
	}
	disk, err := h.read()
	if err != nil {
		t.Fatal(err)
	}
	(*disk.Snapshot.Apps.Value)[0].QuadletUnits = target.Known(release.Units)
	if err = h.write(disk, ""); err != nil {
		t.Fatal(err)
	}
	engine := h.runner.Executor.(Executor).Engine
	engine.Units = recordedRemovalUnits{removalUnits: removalUnits{h.units, h}, recorded: previousHash}
	reconciler := newReconciler(h.service, engine, engine.Systemd.(*systemd.Fake))
	h.runner.Reconciler = runnerReconciler{reconciler}
	h.server.Reconciler = reconciler
	source := seedTerminalRemoval(t, h)
	accepted := h.call(t, "resolve", dispatch.ResolveArgs{OperationID: source, IdempotencyKey: "historical-resolution"}).Data.(jobs.Accepted)
	if err = h.runner.Run(ctx, accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	op, err := h.store.GetOperation(ctx, accepted.OperationID)
	if err != nil || op.State != ops.Succeeded {
		t.Fatal(op, err)
	}
	if _, err = h.store.CurrentRelease(ctx, "hello"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}
