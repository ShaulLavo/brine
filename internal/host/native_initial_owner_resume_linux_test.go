//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/workspaceobserve"
)

func initialOwnerResumeFixture(t *testing.T) (*nativePrepareWork, nativeMaterialFixture, store.NativeInitialOwnerObservation) {
	t.Helper()
	w, f := stoppedUnpublishedPrepareFixture(t)
	ctx := context.Background()
	var err error
	w.parent, w.path, err = newNativePreparedRestoreParent(ctx, w.permit)
	if err != nil {
		t.Fatal(err)
	}
	copyID, err := data.NewID()
	if err != nil {
		t.Fatal(err)
	}
	w.copied, err = copyEngineSource(ctx, w.permit.Reserved.Database, w.path, copyID, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := buildNativeEmptySource(ctx, w.runtime.state, w.runtime.stateRoot, w.stopped, w.copied)
	if err != nil {
		t.Fatal(err)
	}
	empty, err = w.sealedRecoveryEmpty(ctx, empty)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := empty.source.Empty()
	stage, err := nativeRestoreDirectoryIdentity(w.copied.root, ".")
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := spacetimedb.NewPrivateServerEndpoint("127.0.0.1", privateEnginePort)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	publisher := strings.Repeat("a", 64)
	proof, _ := json.Marshal(map[string]any{"purpose": "native_supplied_key_publisher_verification", "endpoint_sha256": endpoint.SHA256(), "method": "GET", "path": "/v1/identity/" + publisher + "/verify", "status": 204, "publisher": publisher, "credential_sha256": w.permit.Runtime.Secrets.Publisher.SHA256, "verified_at": now})
	owner := store.NativeInitialOwnerObservation{Format: "brine-native-initial-owner-provenance-v1", PointID: w.stopped.PointID(), OperationID: w.stopped.OperationID(), BindingID: w.permit.Runtime.BindingID, EpochID: w.permit.Runtime.EpochID, GenerationID: record.GenerationID, SourceSHA256: empty.source.SHA256(), SourceSealSHA256: w.copied.original.hash, AllocationID: w.permit.Allocation.DatabaseID, Runtime: w.permit.Runtime.Clone(), StageDevice: stage.Device, StageInode: stage.Inode, HelperPID: 20, HelperStartTicks: 1, HelperCgroup: "/fixture", ChildPID: 21, ChildStartTicks: 2, ChildBound: true, ChildStopped: true, HelperDisposed: true, Publisher: publisher, CredentialSHA256: w.permit.Runtime.Secrets.Publisher.SHA256, PublisherProofSHA256: managedServiceHash(proof), PublisherProof: proof, StartedAt: now, FinishedAt: now}
	if err := f.service.Store.RecordNativeInitialOwner(ctx, w.stopped, owner); err != nil {
		t.Fatal("fixture owner retention", err)
	}
	w.retainedOwner = &owner
	return w, f, owner
}

func TestRetainedInitialOwnerReopensExactSourceWithoutNativeHelper(t *testing.T) {
	w, _, owner := initialOwnerResumeFixture(t)
	ctx := context.Background()
	empty, err := w.reopenRetainedEmpty(ctx)
	if err != nil || empty.source.SHA256() != owner.SourceSHA256 {
		t.Fatal("recorded source was not reconstructed", err)
	}
	if w.initialStage != nil || w.module.candidate.lease != nil {
		t.Fatal("owner reuse created another native stage")
	}
	actual, err := w.runtime.state.ReadNativeInitialOwner(ctx, w.stopped)
	before, _ := json.Marshal(owner)
	after, _ := json.Marshal(actual)
	if err != nil || string(before) != string(after) {
		t.Fatal("owner receipt was rewritten", err)
	}
	if err := verifyRetainedInitialSource(ctx, w.runtime.stateRoot, w.permit, w.copied.original, owner); err != nil {
		t.Fatal("review cannot verify exact retained source", err)
	}
}

func TestRetainedOwnerOnlyPreparationCanBeReviewedAndResumed(t *testing.T) {
	w, f, owner := initialOwnerResumeFixture(t)
	ctx := context.Background()
	routes := store.NativePreparedRoutes{PointID: w.stopped.PointID(), OperationID: w.operation.ID, App: f.plan.App, PreviousFiles: map[string]string{}, CurrentFiles: map[string]string{}}
	for _, withdrawn := range []bool{false, true} {
		if err := f.service.Store.RecordNativePreparedRoutes(ctx, w.native, routes, withdrawn); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.service.Store.CompleteTask(ctx, w.operation.ID, ops.RecoveryRequired, ops.TaskOutcome{Error: result.InternalError}); err != nil {
		t.Fatal(err)
	}
	if err := w.close(ctx); err != nil {
		t.Fatal(err)
	}
	i, err := f.service.Store.OpenNativeBackupInspection(ctx, w.operation.ID, f.service.Requester)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := i.PreparedExpectation(ctx)
	if err != nil || !expected.NeedsCapture || expected.InitialOwner == nil || expected.InitialOwner.SourceSHA256 != owner.SourceSHA256 || expected.InitialOwner.GenerationID != owner.GenerationID {
		t.Fatal("owner-only phase lost original authority", err)
	}
	review, err := i.Review(ctx, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	resolver, _, err := f.service.Store.CreateOperation(ctx, ops.Intent{Kind: ops.NativeBackupResolve, App: f.plan.App, SecretRef: review.ID, RecoveryOf: w.operation.ID}, f.service.Requester, "resume-earned-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.SetOperationState(ctx, resolver.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	i, err = f.service.Store.ClaimNativeBackupResolution(ctx, resolver.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = i.Close() }()
	life, err := data.AcquireEngineLifetimeLock(ctx, f.stateRoot, w.input.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = life.Release() }()
	current := expected.Stop
	current.FinishedAt = time.Now().UTC()
	stopped, err := i.ResumePrepared(ctx, life, current)
	if err != nil || stopped.PointID() != owner.PointID || stopped.OperationID() != owner.OperationID {
		t.Fatal("resumed owner changed producer", err)
	}
	actual, err := f.service.Store.ReadNativeInitialOwner(ctx, stopped)
	if err != nil || actual.SourceSHA256 != owner.SourceSHA256 {
		t.Fatal("resumed capability lost earned owner", err)
	}
	resumed := nativePrepareWork{runtime: w.runtime, input: w.input, stopped: stopped, permit: expected.Permit, retainedOwner: expected.InitialOwner}
	o := nativeBackupResolutionOperations{runtime: w.runtime}
	if _, err := o.captureRetainedOwner(ctx, i, &resumed); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("owner-only source did not reach missing fixture backup policy", err)
	}
	if resumed.initialStage != nil || resumed.module.candidate.lease != nil {
		t.Fatal("owner continuation created another helper")
	}
	workspaces, err := i.Workspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatal("owner workspace was not tracked", err)
	}
	if err := resumed.close(ctx); !errors.Is(err, workspaceobserve.ErrUnknown) {
		t.Fatal("fixture without enrolled observer manufactured cleanup proof", err)
	}
	workspaces, err = i.Workspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatal("unknown workspace cleanup lost its retained journal", err)
	}
	afterOwner, err := f.service.Store.ReadNativeInitialOwner(ctx, stopped)
	beforeRaw, _ := json.Marshal(owner)
	afterRaw, _ := json.Marshal(afterOwner)
	if err != nil || string(beforeRaw) != string(afterRaw) {
		t.Fatal("owner continuation rewrote its earned proof", err)
	}
	original, err := f.service.Store.GetOperation(ctx, w.operation.ID)
	if err != nil || original.State != ops.RecoveryRequired {
		t.Fatal("resumption changed original failure", err)
	}
}

func TestRetainedInitialOwnerRefusesGenerationSourceAndMaterialDrift(t *testing.T) {
	w, _, owner := initialOwnerResumeFixture(t)
	for _, kind := range []string{"generation", "source", "seal", "material", "runtime"} {
		t.Run(kind, func(t *testing.T) { testRetainedInitialOwnerDrift(t, w, owner, kind) })
	}
}

func testRetainedInitialOwnerDrift(t *testing.T, w *nativePrepareWork, owner store.NativeInitialOwnerObservation, kind string) {
	t.Helper()
	permit := w.permit
	switch kind {
	case "generation":
		owner.GenerationID = strings.Repeat("b", 32)
	case "source":
		owner.SourceSHA256 = strings.Repeat("b", 64)
	case "seal":
		owner.SourceSealSHA256 = strings.Repeat("b", 64)
	case "material":
		permit.Runtime.Secrets.SigningPrivate.SHA256 = strings.Repeat("b", 64)
	case "runtime":
		permit.Runtime.Secrets.Publisher.Reference = "foreign"
	}
	if err := verifyRetainedInitialSource(context.Background(), w.runtime.stateRoot, permit, w.copied.original, owner); err == nil {
		t.Fatal("changed retained source was accepted", kind)
	}
	w.retainedOwner = &owner
	if _, err := w.reopenRetainedEmpty(context.Background()); err == nil && kind != "material" && kind != "runtime" {
		t.Fatal("changed owner selected another source", kind)
	}
}

func TestRetainedInitialOwnerReopenCarriesImmutableSealToPreparedCompletion(t *testing.T) {
	w, _, owner := initialOwnerResumeFixture(t)
	ctx := context.Background()
	stable := engineSourceSealDigest(w.copied.original)
	if stable == owner.SourceSealSHA256 {
		t.Fatal("fixture did not retain a distinct generic owner seal")
	}
	empty, err := w.reopenRetainedEmpty(ctx)
	if err != nil || empty.source.SHA256() != owner.SourceSHA256 {
		t.Fatal("actual retained owner source did not reopen", err)
	}
	actual, err := w.runtime.state.ReadNativeInitialOwner(ctx, w.stopped)
	if err != nil {
		t.Fatal(err)
	}
	sourceSeal := nativePreparedSourceSeal(w.copied.original, w.retainedOwner)
	if sourceSeal != actual.SourceSealSHA256 || sourceSeal == stable {
		t.Fatal("resumed upload/completion would change its protected owner digest")
	}
	if w.initialStage != nil || w.module.candidate.lease != nil {
		t.Fatal("retained source seal required a second native helper")
	}
	before, _ := json.Marshal(owner)
	after, _ := json.Marshal(actual)
	if string(before) != string(after) {
		t.Fatal("retained completion source was proved by rewriting its owner")
	}
}
