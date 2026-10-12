//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

func publicArchiveFixture(t *testing.T) (publicArchiveMaintenance, archiveHost, archive.Plan, *Runtime) {
	t.Helper()
	h, p, _, _ := retainedHostFixture(t)
	workflow := newArchiveService(h.service, h.stateRoot, h.home, h.session, h.executor, h.routes, h.units)
	maintenance := archive.MaintenanceService{Journal: h.service.Store, Host: h, Requester: p.Requester, Authorize: workflow.Authorize, Lock: workflow.Lock}
	credentials := backupcredentials.ArchiveService{Journal: h.service.Store, Files: backupcredentials.Files{Root: filepath.Join(h.stateRoot, "credentials")}, Requester: p.Requester, Scope: h.archiveCredentialScope, Lock: workflow.Lock}
	taskJobs := jobs.Service{Store: h.service.Store, Launcher: &fakeLaunch{}, Requester: p.Requester}
	controller := newPublicArchiveMaintenance(h.service, maintenance, credentials, taskJobs)
	runtime := &Runtime{Authorize: h.service.Authorize, PublicArchiveMaintenance: controller, Runner: jobs.Runner{Store: h.service.Store, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.ArchiveMaintenanceApply: controller.task, ops.ArchiveMaintenanceResolve: controller.task}}, close: func() error { return nil }}
	return controller, h, p, runtime
}
func publicArchiveCall(t *testing.T, h archiveHost, runtime *Runtime, operation string, args any) result.Envelope {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	request, err := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: dispatch.SchemaVersion, Op: operation, RequestID: "archive-boundary", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(request)
	factory := newServerFactory("fixture", "deploy", func(context.Context, string) (*Runtime, error) { return runtime, nil }, nil)
	server := dispatch.NewServer("fixture", nil)
	server.Factory = factory.Build
	response, err := server.Handle(context.Background(), bytes.NewReader(request))
	if err != nil || !response.OK {
		t.Fatal("public archive operation failed", operation, err, response.Error)
	}
	if err = factory.Close(); err != nil {
		t.Fatal(err)
	}
	_ = h
	return response
}
func TestPublicArchiveMaintenanceExpiryUsesExistingJournalAndLeavesRemoteScope(t *testing.T) {
	controller, h, p, runtime := publicArchiveFixture(t)
	ctx := context.Background()
	controller.maintenance.Now = func() time.Time { return time.Now().UTC().Add(31 * 24 * time.Hour) }
	runtime.PublicArchiveMaintenance = controller
	runtime.Runner.TaskHandlers[ops.ArchiveMaintenanceApply] = controller.task
	planned := publicArchiveCall(t, h, runtime, "archive_maintenance_plan", dispatch.ArchiveMaintenancePlanArgs{ArchiveID: p.ArchiveID, Kind: archive.ExpireArchive}).Data.(archive.MaintenancePlan)
	if planned.Kind != archive.ExpireArchive || planned.ArchiveID != p.ArchiveID {
		t.Fatal("wrong reviewed expiry")
	}
	accepted := publicArchiveCall(t, h, runtime, "archive_maintenance_apply", dispatch.ArchiveMaintenanceApplyArgs{ArchiveID: p.ArchiveID, PlanID: planned.ID, IdempotencyKey: strings.Repeat("1", 32)}).Data.(jobs.Accepted)
	if err := runtime.Runner.Run(ctx, accepted.OperationID); err != nil {
		t.Fatal("real detached archive task", err)
	}
	status, err := (jobs.Service{Store: h.service.Store}).Operation(ctx, accepted.OperationID, 0)
	if err != nil || status.Operation.State != ops.Succeeded || status.Outcome == nil {
		t.Fatal("durable maintenance outcome absent", err)
	}
	value, err := ops.DecodeTaskReceipt(status.Operation, status.Outcome.Receipt)
	receipt, ok := value.(archive.MaintenanceReceipt)
	if err != nil || !ok || receipt.Outcome != "local_deleted" || receipt.ArchiveID != p.ArchiveID {
		t.Fatal("actual deletion receipt absent", err)
	}
	archived := filepath.Join(string(p.Trees[0].Root), p.Trees[0].ArchiveRelative)
	if _, err = os.Stat(archived); !os.IsNotExist(err) {
		t.Fatal("reviewed local tree not deleted", err)
	}
	replay := publicArchiveCall(t, h, runtime, "archive_maintenance_apply", dispatch.ArchiveMaintenanceApplyArgs{ArchiveID: p.ArchiveID, PlanID: planned.ID, IdempotencyKey: strings.Repeat("1", 32)}).Data.(jobs.Accepted)
	if replay != accepted {
		t.Fatal("same-key apply created another operation")
	}
	if _, err = controller.ApplyMaintenance(ctx, dispatch.ArchiveMaintenanceApplyArgs{ArchiveID: p.ArchiveID, PlanID: planned.ID, IdempotencyKey: strings.Repeat("2", 32)}); err == nil {
		t.Fatal("different key reran reviewed effect")
	}
	original, err := h.service.Store.LoadArchivePlanByArchive(ctx, p.ArchiveID)
	if err != nil || len(original.Candidate.Databases) != len(p.Candidate.Databases) || len(original.Candidate.Databases[0].ArchiveBindings()) != 1 {
		t.Fatal("local expiry changed original remote epoch record", err)
	}
	view := publicArchiveCall(t, h, runtime, "archive_status", dispatch.ArchiveMaintenanceStatusArgs{ArchiveID: p.ArchiveID}).Data.(archive.MaintenanceStatus)
	if view.State != "local_deleted" {
		t.Fatal("status lost terminal immutable deletion")
	}
}
func TestPublicArchivedRenewalWritesPrivateNamespaceWithoutActivation(t *testing.T) {
	_, h, p, runtime := publicArchiveFixture(t)
	binding := p.Candidate.Databases[0].ArchiveBindings()[0]
	expiry := time.Now().UTC().Add(time.Hour)
	plan := publicArchiveCall(t, h, runtime, "archive_credentials_plan", dispatch.ArchiveCredentialPlanArgs{ArchiveID: string(p.ArchiveID), Binding: string(binding.BindingID), Epoch: string(binding.EpochID), ExpiresAt: &expiry}).Data.(backupcredentials.ArchivePlan)
	wire, err := dispatch.EncodeArchiveCredentialSet(string(p.ArchiveID), string(binding.BindingID), string(binding.EpochID), plan.ID, []byte(`{"access_key_id":"PLANTED_ARCHIVE_ACCESS","secret_access_key":"PLANTED_ARCHIVE_SECRET","expires_at":"`+expiry.Format(time.RFC3339Nano)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(wire)
	receipt := publicArchiveCall(t, h, runtime, "archive_credentials_set", wire).Data.(backupcredentials.ArchiveReceipt)
	if !receipt.Valid() || receipt.Plan.ID != plan.ID {
		t.Fatal("archive delivery receipt changed")
	}
	root := backupcredentials.Files{Root: filepath.Join(h.stateRoot, "credentials")}
	packet, err := root.Read(plan.Scope.FileReference(), plan.Version)
	if err != nil {
		t.Fatal("actual archived private file absent", err)
	}
	defer packet.Clear()
	encoded, _ := json.Marshal(receipt)
	if bytes.Contains(encoded, []byte("PLANTED_ARCHIVE")) {
		t.Fatal("archived credentials leaked into receipt")
	}
	permit, err := h.service.Store.ReadReplicaPermit(context.Background(), binding.DatabaseID)
	if err != nil || permit.FenceState != "held" {
		t.Fatal("archived renewal reactivated a replica or released its fence", err)
	}
}
func TestPublicArchiveMaintenanceDefaultPurgeAndForeignPrincipalRefuse(t *testing.T) {
	controller, h, p, _ := publicArchiveFixture(t)
	if _, err := controller.PlanMaintenance(context.Background(), dispatch.ArchiveMaintenancePlanArgs{ArchiveID: p.ArchiveID, Kind: archive.PurgeArchive}); err == nil {
		t.Fatal("default false purge grant bypassed")
	}
	h.service.Requester = "deploy:" + strings.Repeat("f", 64)
	binding := p.Candidate.Databases[0].ArchiveBindings()[0]
	if _, err := h.archiveCredentialScope(context.Background(), string(p.ArchiveID), string(binding.BindingID), string(binding.EpochID)); err == nil {
		t.Fatal("foreign principal renewed original archive credentials")
	}
}

func TestPublicArchiveRetentionVerificationComesFromExistingHostAndStore(t *testing.T) {
	_, h, p, runtime := publicArchiveFixture(t)
	ctx := context.Background()
	planned := publicArchiveCall(t, h, runtime, "archive_maintenance_plan", dispatch.ArchiveMaintenancePlanArgs{ArchiveID: p.ArchiveID, Kind: archive.VerifyRetention}).Data.(archive.MaintenancePlan)
	accepted := publicArchiveCall(t, h, runtime, "archive_maintenance_apply", dispatch.ArchiveMaintenanceApplyArgs{ArchiveID: p.ArchiveID, PlanID: planned.ID, IdempotencyKey: strings.Repeat("1", 32)}).Data.(jobs.Accepted)
	if err := runtime.Runner.Run(ctx, accepted.OperationID); err != nil {
		t.Fatal("actual existing retention verifier", err)
	}
	op, err := h.service.Store.GetOperation(ctx, accepted.OperationID)
	if err != nil || op.State != ops.Succeeded {
		t.Fatal("verification did not commit outcome", err)
	}
	proof, err := h.service.Store.ReadRetentionProof(ctx, p.ArchiveID)
	if err != nil || proof.ArchiveID != p.ArchiveID || proof.VerifiedAt.IsZero() {
		t.Fatal("actual retained verification absent", err)
	}
	view := publicArchiveCall(t, h, runtime, "archive_status", dispatch.ArchiveMaintenanceStatusArgs{ArchiveID: p.ArchiveID}).Data.(archive.MaintenanceStatus)
	if view.State != "verified" || view.Proof == nil || !view.Proof.VerifiedAt.Equal(proof.VerifiedAt) {
		t.Fatal("status refreshed or lost retained proof")
	}
}

type lostArchiveDeletion struct {
	archive.MaintenanceHost
	once bool
}

func (h *lostArchiveDeletion) DeleteArchivedTree(ctx context.Context, p archive.Plan, m archive.MaintenancePlan, index int, adopt bool) error {
	if err := h.MaintenanceHost.DeleteArchivedTree(ctx, p, m, index, adopt); err != nil {
		return err
	}
	if !h.once {
		h.once = true
		return archive.ErrRecovery
	}
	return nil
}
func TestPublicArchiveResolveInspectsOnlyUnknownOriginalDeletion(t *testing.T) {
	controller, h, p, runtime := publicArchiveFixture(t)
	ctx := context.Background()
	controller.maintenance.Now = func() time.Time { return time.Now().UTC().Add(31 * 24 * time.Hour) }
	controller.maintenance.Host = &lostArchiveDeletion{MaintenanceHost: controller.maintenance.Host}
	runtime.PublicArchiveMaintenance = controller
	runtime.Runner.TaskHandlers[ops.ArchiveMaintenanceApply] = controller.task
	runtime.Runner.TaskHandlers[ops.ArchiveMaintenanceResolve] = controller.task
	planned := publicArchiveCall(t, h, runtime, "archive_maintenance_plan", dispatch.ArchiveMaintenancePlanArgs{ArchiveID: p.ArchiveID, Kind: archive.ExpireArchive}).Data.(archive.MaintenancePlan)
	accepted := publicArchiveCall(t, h, runtime, "archive_maintenance_apply", dispatch.ArchiveMaintenanceApplyArgs{ArchiveID: p.ArchiveID, PlanID: planned.ID, IdempotencyKey: strings.Repeat("1", 32)}).Data.(jobs.Accepted)
	if err := runtime.Runner.Run(ctx, accepted.OperationID); err == nil {
		t.Fatal("lost acknowledgement yielded success")
	}
	original, err := h.service.Store.GetOperation(ctx, accepted.OperationID)
	if err != nil || original.State != ops.RecoveryRequired {
		t.Fatal("unknown original operation not retained", err)
	}
	deletion, err := h.service.Store.ReadArchiveDeletion(ctx, p.ArchiveID)
	if err != nil || deletion.Stage != archive.TreeDeletionIntent {
		t.Fatal("unknown actual deletion intent absent", err)
	}
	resolved := publicArchiveCall(t, h, runtime, "archive_maintenance_resolve", dispatch.ArchiveMaintenanceResolveArgs{OperationID: original.ID, IdempotencyKey: strings.Repeat("2", 32)}).Data.(jobs.Accepted)
	if err = runtime.Runner.Run(ctx, resolved.OperationID); err != nil {
		t.Fatal("typed inspected deletion reconciliation", err)
	}
	preserved, err := h.service.Store.GetOperation(ctx, original.ID)
	if err != nil || preserved.State != ops.RecoveryRequired {
		t.Fatal("resolution rewrote original unknown outcome", err)
	}
	terminal, err := (jobs.Service{Store: h.service.Store}).Operation(ctx, resolved.OperationID, 0)
	if err != nil || terminal.Operation.State != ops.Succeeded || terminal.Outcome == nil {
		t.Fatal("resolved outcome absent", err)
	}
	value, err := ops.DecodeTaskReceipt(terminal.Operation, terminal.Outcome.Receipt)
	receipt, ok := value.(archive.MaintenanceReceipt)
	if err != nil || !ok || receipt.OriginalOperationID != original.ID || receipt.Outcome != "local_deleted" {
		t.Fatal("resolution receipt lost original scope", err)
	}
}

func TestPublicArchiveResolveRefusesUnissuedDeletionAndVerification(t *testing.T) {
	for _, kind := range []archive.MaintenanceKind{archive.VerifyRetention, archive.ExpireArchive} {
		t.Run(string(kind), func(t *testing.T) { refuseUnissuedArchiveResolve(t, kind) })
	}
}
func refuseUnissuedArchiveResolve(t *testing.T, kind archive.MaintenanceKind) {
	t.Helper()
	controller, h, p, runtime := publicArchiveFixture(t)
	ctx := context.Background()
	if kind == archive.ExpireArchive {
		controller.maintenance.Now = func() time.Time { return time.Now().UTC().Add(31 * 24 * time.Hour) }
	}
	runtime.PublicArchiveMaintenance = controller
	planned, err := controller.PlanMaintenance(ctx, dispatch.ArchiveMaintenancePlanArgs{ArchiveID: p.ArchiveID, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := controller.ApplyMaintenance(ctx, dispatch.ArchiveMaintenanceApplyArgs{ArchiveID: p.ArchiveID, PlanID: planned.ID, IdempotencyKey: strings.Repeat("1", 32)})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.service.Store.SetOperationState(ctx, accepted.OperationID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	if err = h.service.Store.CompleteTask(ctx, accepted.OperationID, ops.RecoveryRequired, ops.TaskOutcome{Error: result.RecoveryRequired}); err != nil {
		t.Fatal(err)
	}
	if _, err = controller.ResolveMaintenance(ctx, dispatch.ArchiveMaintenanceResolveArgs{OperationID: accepted.OperationID, IdempotencyKey: strings.Repeat("2", 32)}); err == nil {
		t.Fatal("unissued deletion or verification failure became a deletion resume")
	}
	deletion, err := h.service.Store.ReadArchiveDeletion(ctx, p.ArchiveID)
	if err != nil || deletion.Sequence != 0 {
		t.Fatal("refusal created deletion intent", err)
	}
}
