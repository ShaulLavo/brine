//go:build linux

package host

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/nativematerial"
)

func TestNativeMaterialRenewalAfterPublicationEpochChanges(t *testing.T) {
	f := initializedMaterialFixture(t)
	completeMaterialPublicationFixture(t, f, data.ReplicaEpochID(strings.Repeat("7", 32)))
	completeMaterialPublicationFixture(t, f, data.ReplicaEpochID(strings.Repeat("8", 32)))
	ctx := t.Context()
	before, _, err := f.service.Store.ReadEngineHead(ctx, f.plan.Managed.Database.BindingID)
	if err != nil || before.EpochID == f.plan.Managed.Archive.EpochID {
		t.Fatal("fixture did not advance the publication epoch", err)
	}
	projection, err := f.service.Store.ReadManagedApp(ctx, f.plan.App)
	if err != nil || projection.Reserved.Archive.EpochID != before.EpochID {
		t.Fatal("fixture did not project the published epoch", err)
	}
	packet := freshNativeMaterialPacket(t)
	defer clear(packet)
	delivery := deliverNativeMaterialFixture(t, f, strings.Repeat("4", 32), packet)
	if delivery.EpochID != before.EpochID || delivery.Outcome != "stored_unreferenced" {
		t.Fatal("renewal did not retain current epoch")
	}
	reviewed := materialActivationCall(t, f, "native_material_plan", dispatch.NativeMaterialPlanArgs{App: f.plan.App, DeliveryOperationID: delivery.OperationID}).(nativematerial.ActivationPlan)
	receipt := materialActivationCall(t, f, "native_material_activate", dispatch.NativeMaterialActivateArgs{PlanID: reviewed.ID}).(nativematerial.ActivationReceipt)
	after, active, err := f.service.Store.ReadEngineHead(ctx, before.BindingID)
	if err != nil || after.Version != before.Version+1 || after.EpochID != before.EpochID || active.Secrets != delivery.Secrets || after.RuntimeSHA256 != receipt.RuntimeSHA256 {
		t.Fatal("published-epoch renewal did not activate", err)
	}
	permit, err := f.service.Store.ReadEnginePermit(ctx, before.BindingID)
	if err != nil || permit.Reserved.Archive != projection.Reserved.Archive || !reflect.DeepEqual(permit.Allocation, projection.Permit.Allocation) {
		t.Fatal("activation lost published epoch or allocation", err)
	}
	if replay := deliverNativeMaterialFixture(t, f, strings.Repeat("4", 32), packet); replay != delivery {
		t.Fatal("post-activation delivery replay changed receipt")
	}
}

// Seed a completed publication journal using the domain codecs. No engine or
// remote publication runs in this fixture.
func completeMaterialPublicationFixture(t *testing.T, f nativeMaterialFixture, epoch data.ReplicaEpochID) {
	t.Helper()
	ctx := t.Context()
	head, runtime, err := f.service.Store.ReadEngineHead(ctx, f.plan.Managed.Database.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := f.service.Store.ReadEnginePermit(ctx, head.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	base, err := data.CommittedEngineBase(head)
	if err != nil {
		t.Fatal(err)
	}
	runtime.EpochID = epoch
	raw, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	wanted, err := f.service.Store.ReadManagedDesired(ctx, head.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := f.service.Store.HoldDataFence(ctx, permit.Reserved.Database.DatabaseID, string(epoch))
	if err != nil {
		t.Fatal(err)
	}
	scope := data.EngineTransitionScope{ID: string(epoch), ApplyOperationID: string(epoch), PlanID: wanted.PlanID, Requester: f.service.Requester, PolicyHash: f.desired.PolicyHash, TargetHash: "sha256:" + strings.Repeat("1", 64), DesiredHash: "sha256:" + strings.Repeat("2", 64), IncarnationID: permit.Reserved.Database.IncarnationID, DatabaseID: permit.Reserved.Database.DatabaseID, BindingID: head.BindingID, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	proof := func(epoch data.ReplicaEpochID, kind data.EngineEvidenceKind, n int) data.EngineEvidenceRef {
		return data.EngineEvidenceRef{Kind: kind, ID: string(epoch)[:30] + fmt.Sprintf("%02x", n), SHA256: fmt.Sprintf("%064x", n), DatabaseID: scope.DatabaseID, BindingID: scope.BindingID, EpochID: epoch}
	}
	allocation := *permit.Allocation
	root := data.EngineDirectoryIdentity{Device: allocation.RootDevice, Inode: allocation.RootInode}
	target := data.EngineTreeSeal{Root: root, Parent: root, Directory: data.EngineDirectoryIdentity{Device: allocation.DirectoryDevice, Inode: allocation.DirectoryInode}, ContentsSHA256: strings.Repeat("3", 64)}
	old := target
	old.Directory.Inode++
	common := data.EngineTransitionCommon{Engine: data.EngineSpacetimeDB, Scope: scope, Base: base, NewEpochID: epoch, FenceID: fence.ID, FenceOwnerOperationID: string(epoch), PreparedPoint: proof(head.EpochID, data.EnginePreparedPointEvidence, 1), SourceWitness: proof(head.EpochID, data.EngineSourceWitnessEvidence, 2), OldTree: old, TargetTree: target, PreservedParent: root, TargetRuntimeSHA256: managedServiceHash(raw), TargetVerification: proof(epoch, data.EngineTargetEvidence, 3), TargetCheckpoint: proof(epoch, data.EngineCheckpointEvidence, 4), TargetMaterial: proof(epoch, data.EngineMaterialEvidence, 5), RecoveryRelease: proof(epoch, data.EngineRecoveryEvidence, 6), RetainedDependencies: proof(epoch, data.EngineDependenciesEvidence, 7)}
	transition, err := data.NewEnginePublicationTransition(common, data.EnginePublicationTarget{CandidateSHA256: strings.Repeat("4", 64), Receipt: proof(epoch, data.EnginePublicationEvidence, 8)})
	if err != nil {
		t.Fatal(err)
	}
	archive := permit.Reserved.Archive
	archive.EpochID = epoch
	archive.RemotePrefix, err = data.RemotePrefix(archive.Destination.BasePrefix, scope.IncarnationID, scope.DatabaseID, epoch)
	if err != nil {
		t.Fatal(err)
	}
	archiveRaw, _ := json.Marshal(archive)
	reserved, err := data.NewNativeEpoch(transition, managedServiceHash(archiveRaw), strings.Repeat("5", 64), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.stateRoot, "control.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO operations(id,requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref) VALUES(?,?,?,'succeeded',?,?,'module_publish_apply',?,?)", scope.ApplyOperationID, scope.Requester, string(epoch), time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), f.plan.App, scope.PlanID)
	exec("INSERT INTO data_engine_transition_inputs VALUES(?,?,?,?,?,?)", scope.ID, scope.ApplyOperationID, common.PreparedPoint.ID, scope.BindingID, common.FenceID, transition.Bytes())
	exec("INSERT INTO data_native_epochs VALUES(?,?,?,?,?,?,?,?,?)", epoch, head.BindingID, head.EpochID, scope.ID, archive.Destination.Endpoint, archive.Destination.Bucket, archive.RemotePrefix, archiveRaw, reserved.Bytes())
	nextHead := head
	nextHead.EpochID, nextHead.Version, nextHead.RuntimeSHA256 = epoch, head.Version+1, managedServiceHash(raw)
	exec("INSERT INTO data_engine_runtime_commits VALUES(?,?,?)", head.BindingID, nextHead.Version, raw)
	material, _ := json.Marshal(struct {
		BindingID      data.ReplicaBindingID  `json:"binding_id"`
		EpochID        data.ReplicaEpochID    `json:"epoch_id"`
		RuntimeVersion uint64                 `json:"runtime_version"`
		Allocation     data.AllocationReceipt `json:"allocation"`
		Checkpoint     data.EngineEvidenceRef `json:"checkpoint"`
		Material       data.EngineEvidenceRef `json:"material"`
		TransitionID   string                 `json:"transition_id"`
	}{head.BindingID, epoch, nextHead.Version, allocation, common.TargetCheckpoint, common.TargetMaterial, scope.ID})
	exec("INSERT INTO data_native_epoch_materials VALUES(?,?,?,?)", head.BindingID, epoch, nextHead.Version, material)
	for _, event := range materialPublicationEvents(t, transition, nextHead, proof(epoch, data.EngineCompletionEvidence, 9)) {
		exec("INSERT INTO data_engine_transition_events VALUES(?,?,?,?)", scope.ID, event.Cursor().Sequence, event.Cursor().Stage, event.Bytes())
	}
	wanted.Revision++
	wanted.EpochID, wanted.RuntimeSHA256 = epoch, nextHead.RuntimeSHA256
	desiredRaw, _ := json.Marshal(wanted)
	exec("INSERT INTO data_engine_desired_states VALUES(?,?,?,?,?)", wanted.BindingID, wanted.Revision, wanted.PlanID, wanted.OperationID, desiredRaw)
	exec("UPDATE data_engine_desired_heads SET revision=? WHERE binding_id=?", wanted.Revision, wanted.BindingID)
	exec("UPDATE data_fences SET state='released' WHERE id=?", fence.ID)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, cursor, err := f.service.Store.ReadEngineTransition(ctx, scope.ID)
	if err != nil || cursor.Stage != data.EngineTransitionCompleted {
		t.Fatal("publication fixture is not complete", err)
	}
}

func materialPublicationEvents(t *testing.T, transition data.EngineTransition, head data.EngineHeadRef, proof data.EngineEvidenceRef) []data.EngineTransitionEvent {
	t.Helper()
	event, err := data.PrepareEngineTransition(transition)
	if err != nil {
		t.Fatal(err)
	}
	events := []data.EngineTransitionEvent{event}
	for _, stage := range []data.EngineTransitionStage{data.EngineTransitionPreserveIntent, data.EngineTransitionOriginalPreserved, data.EngineTransitionPromoteIntent, data.EngineTransitionTargetPromoted, data.EngineTransitionActivationIntent, data.EngineTransitionActivated, data.EngineTransitionFinishIntent, data.EngineTransitionCompleted} {
		event, err = nextMaterialPublicationEvent(transition, event.Cursor(), stage, head, proof)
		if err != nil {
			t.Fatal(stage, err)
		}
		events = append(events, event)
	}
	return events
}

func nextMaterialPublicationEvent(transition data.EngineTransition, cursor data.EngineTransitionCursor, stage data.EngineTransitionStage, head data.EngineHeadRef, proof data.EngineEvidenceRef) (data.EngineTransitionEvent, error) {
	if stage == data.EngineTransitionActivated {
		return data.RecordEngineTransitionActivation(transition, cursor, head)
	}
	if stage == data.EngineTransitionCompleted {
		return data.RecordEngineTransitionCompletion(transition, cursor, proof)
	}
	c := transition.Common()
	old, _ := data.PresentEngineLocation(c.OldTree)
	target, _ := data.PresentEngineLocation(c.TargetTree)
	absent, _ := data.AbsentEngineLocation(c.OldTree.Root, c.OldTree.Parent)
	layout := data.EngineLayoutObservation{Live: target, Staged: absent, Preserved: old}
	if stage == data.EngineTransitionPreserveIntent {
		layout = data.EngineLayoutObservation{Live: old, Staged: target, Preserved: absent}
	}
	if stage == data.EngineTransitionOriginalPreserved || stage == data.EngineTransitionPromoteIntent {
		layout = data.EngineLayoutObservation{Live: absent, Staged: target, Preserved: old}
	}
	return data.AdvanceEngineTransition(transition, cursor, stage, layout)
}
